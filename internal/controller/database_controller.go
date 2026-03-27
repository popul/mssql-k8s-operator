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

const requeueInterval = 30 * time.Second

// DatabaseReconciler reconciles a Database object.
type DatabaseReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("Database").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch the Database CR
	var db v1alpha1.Database
	if err := r.Get(ctx, req.NamespacedName, &db); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer handling
	if db.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &db)
	}

	if !controllerutil.ContainsFinalizer(&db, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&db, v1alpha1.Finalizer)
		if err := r.Update(ctx, &db); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Read the credentials Secret
	username, password, err := getCredentialsFromSecret(ctx, r.Client, db.Namespace, db.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", db.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect to SQL Server
	sqlClient, err := connectToSQL(db.Spec.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&db, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 5. Observe current state (with SQL timeout)
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	exists, err := sqlClient.DatabaseExists(sqlCtx, db.Spec.DatabaseName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check database existence: %w", err)
	}

	// 6. Compare and act
	if !exists {
		sqlCtx2, cancel2 := sqlContext(ctx)
		defer cancel2()
		if err := sqlClient.CreateDatabase(sqlCtx2, db.Spec.DatabaseName, db.Spec.Collation); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create database %s: %w", db.Spec.DatabaseName, err)
		}
		r.Recorder.Event(&db, corev1.EventTypeNormal, "DatabaseCreated",
			fmt.Sprintf("Database %s created", db.Spec.DatabaseName))
		logger.Info("database created", "database", db.Spec.DatabaseName)
	}

	// Update owner if specified and different
	if db.Spec.Owner != nil {
		sqlCtx3, cancel3 := sqlContext(ctx)
		defer cancel3()
		currentOwner, err := sqlClient.GetDatabaseOwner(sqlCtx3, db.Spec.DatabaseName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get database owner: %w", err)
		}
		if currentOwner != *db.Spec.Owner {
			sqlCtx4, cancel4 := sqlContext(ctx)
			defer cancel4()
			if err := sqlClient.SetDatabaseOwner(sqlCtx4, db.Spec.DatabaseName, *db.Spec.Owner); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set database owner: %w", err)
			}
			r.Recorder.Event(&db, corev1.EventTypeNormal, "DatabaseOwnerUpdated",
				fmt.Sprintf("Database %s owner set to %s", db.Spec.DatabaseName, *db.Spec.Owner))
		}
	}

	// Check collation drift (immutable after creation)
	if db.Spec.Collation != nil && exists {
		sqlCtx5, cancel5 := sqlContext(ctx)
		defer cancel5()
		currentCollation, err := sqlClient.GetDatabaseCollation(sqlCtx5, db.Spec.DatabaseName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get database collation: %w", err)
		}
		if currentCollation != *db.Spec.Collation {
			r.Recorder.Event(&db, corev1.EventTypeWarning, v1alpha1.ReasonCollationChangeNotSupported,
				fmt.Sprintf("Database %s collation is %s but spec requires %s — collation is immutable",
					db.Spec.DatabaseName, currentCollation, *db.Spec.Collation))
			opmetrics.ReconcileErrors.WithLabelValues("Database", v1alpha1.ReasonCollationChangeNotSupported).Inc()
			return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonCollationChangeNotSupported,
				fmt.Sprintf("Collation mismatch: actual %s, desired %s — collation is immutable after creation",
					currentCollation, *db.Spec.Collation))
		}
	}

	// 7. Status: Ready=True
	opmetrics.ReconcileTotal.WithLabelValues("Database", "success").Inc()
	return r.setConditionAndReturn(ctx, &db, metav1.ConditionTrue, v1alpha1.ReasonReady,
		fmt.Sprintf("Database %s is ready", db.Spec.DatabaseName))
}

func (r *DatabaseReconciler) handleDeletion(ctx context.Context, db *v1alpha1.Database) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(db, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	// Determine deletion policy (default: Retain)
	policy := v1alpha1.DeletionPolicyRetain
	if db.Spec.DeletionPolicy != nil {
		policy = *db.Spec.DeletionPolicy
	}

	if policy == v1alpha1.DeletionPolicyDelete {
		username, password, err := getCredentialsFromSecret(ctx, r.Client, db.Namespace, db.Spec.Server.CredentialsSecret.Name)
		if err != nil {
			logger.Error(err, "failed to get credentials for cleanup, removing finalizer anyway")
		} else {
			sqlClient, err := connectToSQL(db.Spec.Server, username, password, r.SQLClientFactory)
			if err != nil {
				logger.Error(err, "failed to connect to SQL Server for cleanup, removing finalizer anyway")
			} else {
				defer sqlClient.Close()
				sqlCtx, cancel := sqlContext(ctx)
				defer cancel()
				if err := sqlClient.DropDatabase(sqlCtx, db.Spec.DatabaseName); err != nil {
					logger.Error(err, "failed to drop database, removing finalizer anyway")
				} else {
					r.Recorder.Event(db, corev1.EventTypeNormal, "DatabaseDropped",
						fmt.Sprintf("Database %s dropped", db.Spec.DatabaseName))
					logger.Info("database dropped", "database", db.Spec.DatabaseName)
				}
			}
		}
	}

	controllerutil.RemoveFinalizer(db, v1alpha1.Finalizer)
	if err := r.Update(ctx, db); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *DatabaseReconciler) setConditionAndReturn(ctx context.Context, db *v1alpha1.Database,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(db.DeepCopy())

	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: db.Generation,
	})
	db.Status.ObservedGeneration = db.Generation

	if err := r.Status().Patch(ctx, db, patch); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Database{}).
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
