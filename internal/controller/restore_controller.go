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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"golang.org/x/time/rate"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// RestoreReconciler reconciles a Restore object.
type RestoreReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=restores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=restores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=restores/finalizers,verbs=update

func (r *RestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("Restore").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch the Restore CR
	var restore v1alpha1.Restore
	if err := r.Get(ctx, req.NamespacedName, &restore); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. One-shot: skip if already completed or failed
	if restore.Status.Phase == v1alpha1.RestorePhaseCompleted || restore.Status.Phase == v1alpha1.RestorePhaseFailed {
		return ctrl.Result{}, nil
	}

	// 3. Read the credentials Secret
	username, password, err := getCredentialsFromSecret(ctx, r.Client, restore.Namespace, restore.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setRestoreStatus(ctx, &restore, v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", restore.Spec.Server.CredentialsSecret.Name))
		}
		return r.setRestoreStatus(ctx, &restore, v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect to SQL Server
	sqlClient, err := connectToSQL(restore.Spec.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&restore, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 5. Transition to Running
	now := metav1.Now()
	if restore.Status.Phase != v1alpha1.RestorePhaseRunning {
		if _, err := r.setRestoreStatus(ctx, &restore, v1alpha1.RestorePhaseRunning, metav1.ConditionFalse, "RestoreRunning",
			fmt.Sprintf("Restore of %s started", restore.Spec.DatabaseName)); err != nil {
			return ctrl.Result{}, err
		}
		// Re-fetch after status update
		if err := r.Get(ctx, req.NamespacedName, &restore); err != nil {
			return ctrl.Result{}, err
		}
		if restore.Status.StartTime == nil {
			patch := client.MergeFrom(restore.DeepCopy())
			restore.Status.StartTime = &now
			if err := r.Status().Patch(ctx, &restore, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 6. Execute restore (with optional PIT and file moves)
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	var restoreErr error
	if restore.Spec.StopAt != nil {
		restoreErr = sqlClient.RestoreDatabasePIT(sqlCtx, restore.Spec.DatabaseName, restore.Spec.Source, *restore.Spec.StopAt)
	} else if len(restore.Spec.WithMove) > 0 {
		moves := make(map[string]string, len(restore.Spec.WithMove))
		for _, m := range restore.Spec.WithMove {
			moves[m.LogicalName] = m.PhysicalPath
		}
		restoreErr = sqlClient.RestoreDatabaseWithMove(sqlCtx, restore.Spec.DatabaseName, restore.Spec.Source, moves)
	} else {
		restoreErr = sqlClient.RestoreDatabase(sqlCtx, restore.Spec.DatabaseName, restore.Spec.Source)
	}

	if err := restoreErr; err != nil {
		r.Recorder.Event(&restore, corev1.EventTypeWarning, "RestoreFailed", err.Error())
		opmetrics.ReconcileErrors.WithLabelValues("Restore", "RestoreFailed").Inc()
		return r.setRestoreStatus(ctx, &restore, v1alpha1.RestorePhaseFailed, metav1.ConditionFalse, "RestoreFailed",
			fmt.Sprintf("Restore failed: %v", err))
	}

	// 7. Success
	r.Recorder.Event(&restore, corev1.EventTypeNormal, "RestoreCompleted",
		fmt.Sprintf("Restore of %s completed from %s", restore.Spec.DatabaseName, restore.Spec.Source))
	logger.Info("restore completed", "database", restore.Spec.DatabaseName, "source", restore.Spec.Source)

	// Set completion time and phase
	completionTime := metav1.Now()
	// Re-fetch before final update
	if err := r.Get(ctx, req.NamespacedName, &restore); err != nil {
		return ctrl.Result{}, err
	}
	patch := client.MergeFrom(restore.DeepCopy())
	restore.Status.Phase = v1alpha1.RestorePhaseCompleted
	restore.Status.CompletionTime = &completionTime
	restore.Status.ObservedGeneration = restore.Generation
	meta.SetStatusCondition(&restore.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "RestoreCompleted",
		Message:            fmt.Sprintf("Restore of %s completed", restore.Spec.DatabaseName),
		ObservedGeneration: restore.Generation,
	})
	if err := r.Status().Patch(ctx, &restore, patch); err != nil {
		return ctrl.Result{}, err
	}

	opmetrics.ReconcileTotal.WithLabelValues("Restore", "success").Inc()
	return ctrl.Result{}, nil
}

func (r *RestoreReconciler) setRestoreStatus(ctx context.Context, restore *v1alpha1.Restore,
	phase v1alpha1.RestorePhase, condStatus metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(restore.DeepCopy())
	restore.Status.Phase = phase
	restore.Status.ObservedGeneration = restore.Generation

	meta.SetStatusCondition(&restore.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: restore.Generation,
	})

	if err := r.Status().Patch(ctx, restore, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *RestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Restore{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToRestores(context.Background(), mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
