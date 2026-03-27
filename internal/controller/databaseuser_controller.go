package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"golang.org/x/time/rate"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// DatabaseUserReconciler reconciles a DatabaseUser object.
type DatabaseUserReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=databaseusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databaseusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databaseusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *DatabaseUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("DatabaseUser").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch
	var dbUser v1alpha1.DatabaseUser
	if err := r.Get(ctx, req.NamespacedName, &dbUser); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer
	if dbUser.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &dbUser)
	}

	if !controllerutil.ContainsFinalizer(&dbUser, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&dbUser, v1alpha1.Finalizer)
		if err := r.Update(ctx, &dbUser); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Resolve LoginRef
	var login v1alpha1.Login
	if err := r.Get(ctx, types.NamespacedName{Name: dbUser.Spec.LoginRef.Name, Namespace: dbUser.Namespace}, &login); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionFalse, v1alpha1.ReasonLoginRefNotFound,
				fmt.Sprintf("Login CR %q not found", dbUser.Spec.LoginRef.Name))
		}
		return ctrl.Result{}, err
	}

	// 4. Read credentials Secret
	username, password, err := getCredentialsFromSecret(ctx, r.Client, dbUser.Namespace, dbUser.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", dbUser.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 5. Connect
	sqlClient, err := connectToSQL(dbUser.Spec.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&dbUser, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 6. Observe (with SQL timeout)
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	exists, err := sqlClient.UserExists(sqlCtx, dbUser.Spec.DatabaseName, dbUser.Spec.UserName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check user existence: %w", err)
	}

	// 7. Compare & Act
	loginName := login.Spec.LoginName
	if !exists {
		sqlCtx2, cancel2 := sqlContext(ctx)
		defer cancel2()
		if err := sqlClient.CreateUser(sqlCtx2, dbUser.Spec.DatabaseName, dbUser.Spec.UserName, loginName); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create user %s: %w", dbUser.Spec.UserName, err)
		}
		r.Recorder.Event(&dbUser, corev1.EventTypeNormal, "DatabaseUserCreated",
			fmt.Sprintf("User %s created in database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
		logger.Info("database user created", "user", dbUser.Spec.UserName, "database", dbUser.Spec.DatabaseName)
	}

	// Reconcile database roles
	if err := r.reconcileDatabaseRoles(ctx, &dbUser, sqlClient, dbUser.Spec.DatabaseName, dbUser.Spec.UserName, dbUser.Spec.DatabaseRoles); err != nil {
		opmetrics.ReconcileErrors.WithLabelValues("DatabaseUser", "DatabaseRoleReconciliation").Inc()
		return ctrl.Result{}, err
	}

	// 8. Status
	opmetrics.ReconcileTotal.WithLabelValues("DatabaseUser", "success").Inc()
	return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionTrue, v1alpha1.ReasonReady,
		fmt.Sprintf("User %s is ready in database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
}

func (r *DatabaseUserReconciler) reconcileDatabaseRoles(ctx context.Context, dbUser *v1alpha1.DatabaseUser, sqlClient sqlclient.SQLClient, dbName, userName string, desiredRoles []string) error {
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	currentRoles, err := sqlClient.GetUserDatabaseRoles(sqlCtx, dbName, userName)
	if err != nil {
		return fmt.Errorf("failed to get database roles: %w", err)
	}

	currentSet := toSet(currentRoles)
	desiredSet := toSet(desiredRoles)

	// Add missing roles
	for role := range desiredSet {
		if !currentSet[role] {
			sqlCtx2, cancel2 := sqlContext(ctx)
			defer cancel2()
			if err := sqlClient.AddUserToDatabaseRole(sqlCtx2, dbName, userName, role); err != nil {
				return fmt.Errorf("failed to add role %s: %w", role, err)
			}
			r.Recorder.Event(dbUser, corev1.EventTypeNormal, "DatabaseRoleAdded",
				fmt.Sprintf("User %s added to database role %s", userName, role))
		}
	}

	// Remove extra roles
	for role := range currentSet {
		if !desiredSet[role] {
			sqlCtx3, cancel3 := sqlContext(ctx)
			defer cancel3()
			if err := sqlClient.RemoveUserFromDatabaseRole(sqlCtx3, dbName, userName, role); err != nil {
				return fmt.Errorf("failed to remove role %s: %w", role, err)
			}
			r.Recorder.Event(dbUser, corev1.EventTypeNormal, "DatabaseRoleRemoved",
				fmt.Sprintf("User %s removed from database role %s", userName, role))
		}
	}

	return nil
}

func (r *DatabaseUserReconciler) handleDeletion(ctx context.Context, dbUser *v1alpha1.DatabaseUser) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(dbUser, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	username, password, err := getCredentialsFromSecret(ctx, r.Client, dbUser.Namespace, dbUser.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		logger.Error(err, "failed to get credentials for cleanup")
	} else {
		sqlClient, err := connectToSQL(dbUser.Spec.Server, username, password, r.SQLClientFactory)
		if err != nil {
			logger.Error(err, "failed to connect for cleanup")
		} else {
			defer sqlClient.Close()

			// Check if user owns objects
			sqlCtx, cancel := sqlContext(ctx)
			defer cancel()
			owns, err := sqlClient.UserOwnsObjects(sqlCtx, dbUser.Spec.DatabaseName, dbUser.Spec.UserName)
			if err != nil {
				logger.Error(err, "failed to check if user owns objects")
			} else if owns {
				r.Recorder.Event(dbUser, corev1.EventTypeWarning, v1alpha1.ReasonUserOwnsObjects,
					fmt.Sprintf("User %s owns objects in database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
				return r.setConditionAndReturn(ctx, dbUser, metav1.ConditionFalse, v1alpha1.ReasonUserOwnsObjects,
					"User owns objects in the database, transfer ownership first")
			}

			sqlCtx2, cancel2 := sqlContext(ctx)
			defer cancel2()
			if err := sqlClient.DropUser(sqlCtx2, dbUser.Spec.DatabaseName, dbUser.Spec.UserName); err != nil {
				logger.Error(err, "failed to drop user")
			} else {
				r.Recorder.Event(dbUser, corev1.EventTypeNormal, "DatabaseUserDropped",
					fmt.Sprintf("User %s dropped from database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
			}
		}
	}

	controllerutil.RemoveFinalizer(dbUser, v1alpha1.Finalizer)
	if err := r.Update(ctx, dbUser); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *DatabaseUserReconciler) setConditionAndReturn(ctx context.Context, dbUser *v1alpha1.DatabaseUser,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(dbUser.DeepCopy())

	meta.SetStatusCondition(&dbUser.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: dbUser.Generation,
	})
	dbUser.Status.ObservedGeneration = dbUser.Generation

	if err := r.Status().Patch(ctx, dbUser, patch); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DatabaseUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DatabaseUser{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
