package controller

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

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

	// 3. Resolve server reference (supports sqlServerRef)
	serverRef, err := resolveServerReference(ctx, r.Client, db.Namespace, db.Spec.Server)
	if err != nil {
		return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonConnectionFailed,
			fmt.Sprintf("Failed to resolve server reference: %v", err))
	}

	// 4. Read the credentials Secret
	username, password, err := getCredentialsFromSecret(ctx, r.Client, db.Namespace, serverRef.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", serverRef.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 5. Connect to SQL Server
	sqlClient, err := connectToSQL(serverRef, username, password, r.SQLClientFactory)
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

	// Reconcile recovery model
	if db.Spec.RecoveryModel != nil && exists {
		sqlCtxRM, cancelRM := sqlContext(ctx)
		defer cancelRM()
		currentRM, err := sqlClient.GetDatabaseRecoveryModel(sqlCtxRM, db.Spec.DatabaseName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get recovery model: %w", err)
		}
		desiredRM := string(*db.Spec.RecoveryModel)
		// SQL Server returns "FULL", "SIMPLE", "BULK_LOGGED"
		desiredRMSQL := desiredRM
		if desiredRM == "BulkLogged" {
			desiredRMSQL = "BULK_LOGGED"
		}
		if currentRM != desiredRMSQL {
			sqlCtxRM2, cancelRM2 := sqlContext(ctx)
			defer cancelRM2()
			if err := sqlClient.SetDatabaseRecoveryModel(sqlCtxRM2, db.Spec.DatabaseName, desiredRMSQL); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set recovery model: %w", err)
			}
			r.Recorder.Eventf(&db, corev1.EventTypeNormal, "RecoveryModelUpdated",
				"Database %s recovery model set to %s", db.Spec.DatabaseName, desiredRM)
		}
	}

	// Reconcile compatibility level
	if db.Spec.CompatibilityLevel != nil && exists {
		sqlCtxCL, cancelCL := sqlContext(ctx)
		defer cancelCL()
		currentCL, err := sqlClient.GetDatabaseCompatibilityLevel(sqlCtxCL, db.Spec.DatabaseName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get compatibility level: %w", err)
		}
		desiredCL := int(*db.Spec.CompatibilityLevel)
		if currentCL != desiredCL {
			sqlCtxCL2, cancelCL2 := sqlContext(ctx)
			defer cancelCL2()
			if err := sqlClient.SetDatabaseCompatibilityLevel(sqlCtxCL2, db.Spec.DatabaseName, desiredCL); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set compatibility level: %w", err)
			}
			r.Recorder.Eventf(&db, corev1.EventTypeNormal, "CompatibilityLevelUpdated",
				"Database %s compatibility level set to %d", db.Spec.DatabaseName, desiredCL)
		}
	}

	// Reconcile database options
	for _, opt := range db.Spec.Options {
		sqlCtxOpt, cancelOpt := sqlContext(ctx)
		currentVal, err := sqlClient.GetDatabaseOption(sqlCtxOpt, db.Spec.DatabaseName, opt.Name)
		cancelOpt()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get database option %s: %w", opt.Name, err)
		}
		if currentVal != opt.Value {
			sqlCtxOpt2, cancelOpt2 := sqlContext(ctx)
			if err := sqlClient.SetDatabaseOption(sqlCtxOpt2, db.Spec.DatabaseName, opt.Name, opt.Value); err != nil {
				cancelOpt2()
				return ctrl.Result{}, fmt.Errorf("failed to set database option %s: %w", opt.Name, err)
			}
			cancelOpt2()
			r.Recorder.Eventf(&db, corev1.EventTypeNormal, "DatabaseOptionUpdated",
				"Database %s option %s set to %v", db.Spec.DatabaseName, opt.Name, opt.Value)
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
	opmetrics.DatabaseState.WithLabelValues(db.Name, db.Namespace, db.Spec.DatabaseName, db.Spec.Server.Host).Set(1)
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
		For(&v1alpha1.Database{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToDatabases(mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
