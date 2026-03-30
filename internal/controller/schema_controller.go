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

// SchemaReconciler reconciles a Schema object.
type SchemaReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=schemas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=schemas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=schemas/finalizers,verbs=update

func (r *SchemaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("Schema").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch
	var schema v1alpha1.Schema
	if err := r.Get(ctx, req.NamespacedName, &schema); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer
	if schema.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &schema)
	}

	if !controllerutil.ContainsFinalizer(&schema, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&schema, v1alpha1.Finalizer)
		if err := r.Update(ctx, &schema); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Read credentials
	username, password, err := getCredentialsFromSecret(ctx, r.Client, schema.Namespace, schema.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &schema, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", schema.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &schema, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect
	sqlClient, err := connectToSQL(schema.Spec.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&schema, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 5. Observe
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	exists, err := sqlClient.SchemaExists(sqlCtx, schema.Spec.DatabaseName, schema.Spec.SchemaName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check schema existence: %w", err)
	}

	// 6. Compare & Act
	if !exists {
		sqlCtx2, cancel2 := sqlContext(ctx)
		defer cancel2()
		if err := sqlClient.CreateSchema(sqlCtx2, schema.Spec.DatabaseName, schema.Spec.SchemaName, schema.Spec.Owner); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create schema %s: %w", schema.Spec.SchemaName, err)
		}
		r.Recorder.Event(&schema, corev1.EventTypeNormal, "SchemaCreated",
			fmt.Sprintf("Schema %s created in database %s", schema.Spec.SchemaName, schema.Spec.DatabaseName))
		logger.Info("schema created", "schema", schema.Spec.SchemaName, "database", schema.Spec.DatabaseName)
	}

	// Reconcile owner
	if schema.Spec.Owner != nil {
		sqlCtx3, cancel3 := sqlContext(ctx)
		defer cancel3()
		currentOwner, err := sqlClient.GetSchemaOwner(sqlCtx3, schema.Spec.DatabaseName, schema.Spec.SchemaName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get schema owner: %w", err)
		}
		if currentOwner != *schema.Spec.Owner {
			sqlCtx4, cancel4 := sqlContext(ctx)
			defer cancel4()
			if err := sqlClient.SetSchemaOwner(sqlCtx4, schema.Spec.DatabaseName, schema.Spec.SchemaName, *schema.Spec.Owner); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set schema owner: %w", err)
			}
			r.Recorder.Event(&schema, corev1.EventTypeNormal, "SchemaOwnerUpdated",
				fmt.Sprintf("Schema %s owner set to %s", schema.Spec.SchemaName, *schema.Spec.Owner))
		}
	}

	// 7. Status
	opmetrics.ReconcileTotal.WithLabelValues("Schema", "success").Inc()
	return r.setConditionAndReturn(ctx, &schema, metav1.ConditionTrue, v1alpha1.ReasonReady,
		fmt.Sprintf("Schema %s is ready in database %s", schema.Spec.SchemaName, schema.Spec.DatabaseName))
}

func (r *SchemaReconciler) handleDeletion(ctx context.Context, schema *v1alpha1.Schema) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(schema, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	policy := v1alpha1.DeletionPolicyRetain
	if schema.Spec.DeletionPolicy != nil {
		policy = *schema.Spec.DeletionPolicy
	}

	if policy == v1alpha1.DeletionPolicyDelete {
		username, password, err := getCredentialsFromSecret(ctx, r.Client, schema.Namespace, schema.Spec.Server.CredentialsSecret.Name)
		if err != nil {
			logger.Error(err, "failed to get credentials for cleanup, removing finalizer anyway")
		} else {
			sqlClient, err := connectToSQL(schema.Spec.Server, username, password, r.SQLClientFactory)
			if err != nil {
				logger.Error(err, "failed to connect for cleanup, removing finalizer anyway")
			} else {
				defer sqlClient.Close()

				// Check if schema has objects
				sqlCtx, cancel := sqlContext(ctx)
				defer cancel()
				hasObjects, err := sqlClient.SchemaHasObjects(sqlCtx, schema.Spec.DatabaseName, schema.Spec.SchemaName)
				if err != nil {
					logger.Error(err, "failed to check if schema has objects")
				} else if hasObjects {
					r.Recorder.Event(schema, corev1.EventTypeWarning, v1alpha1.ReasonSchemaNotEmpty,
						fmt.Sprintf("Schema %s contains objects in database %s", schema.Spec.SchemaName, schema.Spec.DatabaseName))
					patch := client.MergeFrom(schema.DeepCopy())
					meta.SetStatusCondition(&schema.Status.Conditions, metav1.Condition{
						Type:               v1alpha1.ConditionReady,
						Status:             metav1.ConditionFalse,
						Reason:             v1alpha1.ReasonSchemaNotEmpty,
						Message:            "Schema contains objects, move or drop them first",
						ObservedGeneration: schema.Generation,
					})
					_ = r.Status().Patch(ctx, schema, patch)
					return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
				}

				sqlCtx2, cancel2 := sqlContext(ctx)
				defer cancel2()
				if err := sqlClient.DropSchema(sqlCtx2, schema.Spec.DatabaseName, schema.Spec.SchemaName); err != nil {
					logger.Error(err, "failed to drop schema, removing finalizer anyway")
				} else {
					r.Recorder.Event(schema, corev1.EventTypeNormal, "SchemaDropped",
						fmt.Sprintf("Schema %s dropped from database %s", schema.Spec.SchemaName, schema.Spec.DatabaseName))
				}
			}
		}
	}

	controllerutil.RemoveFinalizer(schema, v1alpha1.Finalizer)
	if err := r.Update(ctx, schema); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *SchemaReconciler) setConditionAndReturn(ctx context.Context, schema *v1alpha1.Schema,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(schema.DeepCopy())

	meta.SetStatusCondition(&schema.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: schema.Generation,
	})
	schema.Status.ObservedGeneration = schema.Generation

	if err := r.Status().Patch(ctx, schema, patch); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
	}
	return ctrl.Result{}, nil
}

func (r *SchemaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Schema{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToSchemas(mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
