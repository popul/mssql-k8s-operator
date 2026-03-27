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

// LoginReconciler reconciles a Login object.
type LoginReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *LoginReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("Login").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch
	var login v1alpha1.Login
	if err := r.Get(ctx, req.NamespacedName, &login); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer
	if login.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &login)
	}

	if !controllerutil.ContainsFinalizer(&login, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&login, v1alpha1.Finalizer)
		if err := r.Update(ctx, &login); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Read credentials Secret
	username, saPassword, err := getCredentialsFromSecret(ctx, r.Client, login.Namespace, login.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", login.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Read password Secret
	var pwSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: login.Spec.PasswordSecret.Name, Namespace: login.Namespace}, &pwSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Password secret %q not found", login.Spec.PasswordSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonConnectionFailed,
			fmt.Sprintf("Failed to fetch password secret: %v", err))
	}
	loginPassword, ok := pwSecret.Data["password"]
	if !ok {
		return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret,
			fmt.Sprintf("Password secret %q missing 'password' key", login.Spec.PasswordSecret.Name))
	}

	// 5. Connect
	sqlClient, err := connectToSQL(login.Spec.Server, username, saPassword, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&login, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 6. Observe (with SQL timeout)
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	exists, err := sqlClient.LoginExists(sqlCtx, login.Spec.LoginName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check login existence: %w", err)
	}

	// 7. Compare & Act
	if !exists {
		sqlCtx2, cancel2 := sqlContext(ctx)
		defer cancel2()
		if err := sqlClient.CreateLogin(sqlCtx2, login.Spec.LoginName, string(loginPassword)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create login %s: %w", login.Spec.LoginName, err)
		}
		r.Recorder.Event(&login, corev1.EventTypeNormal, "LoginCreated",
			fmt.Sprintf("Login %s created", login.Spec.LoginName))
		logger.Info("login created", "login", login.Spec.LoginName)
	} else {
		// Check password rotation
		if pwSecret.ResourceVersion != login.Status.PasswordSecretResourceVersion {
			sqlCtx3, cancel3 := sqlContext(ctx)
			defer cancel3()
			if err := sqlClient.UpdateLoginPassword(sqlCtx3, login.Spec.LoginName, string(loginPassword)); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update login password: %w", err)
			}
			r.Recorder.Event(&login, corev1.EventTypeNormal, "LoginPasswordRotated",
				fmt.Sprintf("Login %s password rotated", login.Spec.LoginName))
			logger.Info("login password rotated", "login", login.Spec.LoginName)
		}
	}

	// Default database
	if login.Spec.DefaultDatabase != nil {
		sqlCtx4, cancel4 := sqlContext(ctx)
		defer cancel4()
		currentDB, err := sqlClient.GetLoginDefaultDatabase(sqlCtx4, login.Spec.LoginName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get default database: %w", err)
		}
		if currentDB != *login.Spec.DefaultDatabase {
			sqlCtx5, cancel5 := sqlContext(ctx)
			defer cancel5()
			if err := sqlClient.SetLoginDefaultDatabase(sqlCtx5, login.Spec.LoginName, *login.Spec.DefaultDatabase); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set default database: %w", err)
			}
		}
	}

	// Server roles: compute diff
	if err := r.reconcileServerRoles(ctx, &login, sqlClient, login.Spec.LoginName, login.Spec.ServerRoles); err != nil {
		opmetrics.ReconcileErrors.WithLabelValues("Login", "ServerRoleReconciliation").Inc()
		return ctrl.Result{}, err
	}

	// 8. Status
	opmetrics.ReconcileTotal.WithLabelValues("Login", "success").Inc()

	patch := client.MergeFrom(login.DeepCopy())
	login.Status.PasswordSecretResourceVersion = pwSecret.ResourceVersion
	meta.SetStatusCondition(&login.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1alpha1.ReasonReady,
		Message:            fmt.Sprintf("Login %s is ready", login.Spec.LoginName),
		ObservedGeneration: login.Generation,
	})
	login.Status.ObservedGeneration = login.Generation

	if err := r.Status().Patch(ctx, &login, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
}

func (r *LoginReconciler) reconcileServerRoles(ctx context.Context, login *v1alpha1.Login, sqlClient sqlclient.SQLClient, loginName string, desiredRoles []string) error {
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	currentRoles, err := sqlClient.GetLoginServerRoles(sqlCtx, loginName)
	if err != nil {
		return fmt.Errorf("failed to get server roles: %w", err)
	}

	currentSet := toSet(currentRoles)
	desiredSet := toSet(desiredRoles)

	for role := range desiredSet {
		if !currentSet[role] {
			sqlCtx2, cancel2 := sqlContext(ctx)
			defer cancel2()
			if err := sqlClient.AddLoginToServerRole(sqlCtx2, loginName, role); err != nil {
				return fmt.Errorf("failed to add role %s: %w", role, err)
			}
			r.Recorder.Event(login, corev1.EventTypeNormal, "ServerRoleAdded",
				fmt.Sprintf("Login %s added to server role %s", loginName, role))
		}
	}

	for role := range currentSet {
		if !desiredSet[role] {
			sqlCtx3, cancel3 := sqlContext(ctx)
			defer cancel3()
			if err := sqlClient.RemoveLoginFromServerRole(sqlCtx3, loginName, role); err != nil {
				return fmt.Errorf("failed to remove role %s: %w", role, err)
			}
			r.Recorder.Event(login, corev1.EventTypeNormal, "ServerRoleRemoved",
				fmt.Sprintf("Login %s removed from server role %s", loginName, role))
		}
	}

	return nil
}

func (r *LoginReconciler) handleDeletion(ctx context.Context, login *v1alpha1.Login) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(login, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	policy := v1alpha1.DeletionPolicyRetain
	if login.Spec.DeletionPolicy != nil {
		policy = *login.Spec.DeletionPolicy
	}

	if policy == v1alpha1.DeletionPolicyDelete {
		username, password, err := getCredentialsFromSecret(ctx, r.Client, login.Namespace, login.Spec.Server.CredentialsSecret.Name)
		if err != nil {
			logger.Error(err, "failed to get credentials for cleanup")
		} else {
			sqlClient, err := connectToSQL(login.Spec.Server, username, password, r.SQLClientFactory)
			if err != nil {
				logger.Error(err, "failed to connect for cleanup")
			} else {
				defer sqlClient.Close()

				sqlCtx, cancel := sqlContext(ctx)
				defer cancel()
				hasUsers, err := sqlClient.LoginHasUsers(sqlCtx, login.Spec.LoginName)
				if err != nil {
					logger.Error(err, "failed to check if login has users")
				} else if hasUsers {
					r.Recorder.Event(login, corev1.EventTypeWarning, v1alpha1.ReasonLoginInUse,
						fmt.Sprintf("Login %s is still in use by database users", login.Spec.LoginName))
					// Set condition but requeue — don't block deletion indefinitely
					patch := client.MergeFrom(login.DeepCopy())
					meta.SetStatusCondition(&login.Status.Conditions, metav1.Condition{
						Type:               v1alpha1.ConditionReady,
						Status:             metav1.ConditionFalse,
						Reason:             v1alpha1.ReasonLoginInUse,
						Message:            "Login is still in use by database users, delete DatabaseUser CRs first",
						ObservedGeneration: login.Generation,
					})
					_ = r.Status().Patch(ctx, login, patch)
					return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
				}

				sqlCtx2, cancel2 := sqlContext(ctx)
				defer cancel2()
				if err := sqlClient.DropLogin(sqlCtx2, login.Spec.LoginName); err != nil {
					logger.Error(err, "failed to drop login")
				} else {
					r.Recorder.Event(login, corev1.EventTypeNormal, "LoginDropped",
						fmt.Sprintf("Login %s dropped", login.Spec.LoginName))
				}
			}
		}
	}

	controllerutil.RemoveFinalizer(login, v1alpha1.Finalizer)
	if err := r.Update(ctx, login); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *LoginReconciler) setConditionAndReturn(ctx context.Context, login *v1alpha1.Login,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(login.DeepCopy())

	meta.SetStatusCondition(&login.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: login.Generation,
	})
	login.Status.ObservedGeneration = login.Generation

	if err := r.Status().Patch(ctx, login, patch); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
	}
	return ctrl.Result{}, nil
}

func (r *LoginReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Login{}).
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
