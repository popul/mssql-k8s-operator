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
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"golang.org/x/time/rate"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// AGFailoverReconciler reconciles an AGFailover object.
type AGFailoverReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=agfailovers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=agfailovers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=agfailovers/finalizers,verbs=update

func (r *AGFailoverReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("AGFailover").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch the AGFailover CR
	var fo v1alpha1.AGFailover
	if err := r.Get(ctx, req.NamespacedName, &fo); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. One-shot: skip if already completed or failed
	if fo.Status.Phase == v1alpha1.FailoverPhaseCompleted || fo.Status.Phase == v1alpha1.FailoverPhaseFailed {
		return ctrl.Result{}, nil
	}

	// 3. Read the credentials Secret
	username, password, err := getCredentialsFromSecret(ctx, r.Client, fo.Namespace, fo.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setFailoverStatus(ctx, &fo, v1alpha1.FailoverPhaseFailed, metav1.ConditionFalse,
				v1alpha1.ReasonSecretNotFound, fmt.Sprintf("Secret %q not found", fo.Spec.Server.CredentialsSecret.Name))
		}
		return r.setFailoverStatus(ctx, &fo, v1alpha1.FailoverPhaseFailed, metav1.ConditionFalse,
			v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect to the TARGET replica (the one we want to promote)
	sqlClient, err := connectToSQL(fo.Spec.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to target replica")
		r.Recorder.Event(&fo, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to target replica: %w", err)
	}
	defer sqlClient.Close()

	// 5. Verify the target is a SECONDARY
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	role, err := sqlClient.GetAGReplicaRole(sqlCtx, fo.Spec.AGName, fo.Spec.TargetReplica)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get replica role: %w", err)
	}

	if role == "PRIMARY" {
		// Target is already primary — mark completed
		return r.setFailoverCompleted(ctx, &fo, fo.Spec.TargetReplica, fo.Spec.TargetReplica,
			"Target is already PRIMARY, no failover needed")
	}

	if role != "SECONDARY" {
		return r.setFailoverStatus(ctx, &fo, v1alpha1.FailoverPhaseFailed, metav1.ConditionFalse,
			"TargetNotSecondary", fmt.Sprintf("Target %s has role %s, expected SECONDARY", fo.Spec.TargetReplica, role))
	}

	// 6. Transition to Running
	now := metav1.Now()
	if fo.Status.Phase != v1alpha1.FailoverPhaseRunning {
		if _, err := r.setFailoverStatus(ctx, &fo, v1alpha1.FailoverPhaseRunning, metav1.ConditionFalse,
			"FailoverRunning", fmt.Sprintf("Failover of AG %s to %s started", fo.Spec.AGName, fo.Spec.TargetReplica)); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, req.NamespacedName, &fo); err != nil {
			return ctrl.Result{}, err
		}
		if fo.Status.StartTime == nil {
			patch := client.MergeFrom(fo.DeepCopy())
			fo.Status.StartTime = &now
			if err := r.Status().Patch(ctx, &fo, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 7. Execute failover
	force := fo.Spec.Force != nil && *fo.Spec.Force
	sqlCtx2, cancel2 := sqlContext(ctx)
	defer cancel2()

	if force {
		err = sqlClient.ForceFailoverAG(sqlCtx2, fo.Spec.AGName)
	} else {
		err = sqlClient.FailoverAG(sqlCtx2, fo.Spec.AGName)
	}

	if err != nil {
		failType := "FailoverFailed"
		r.Recorder.Event(&fo, corev1.EventTypeWarning, failType, err.Error())
		opmetrics.ReconcileErrors.WithLabelValues("AGFailover", failType).Inc()
		return r.setFailoverStatus(ctx, &fo, v1alpha1.FailoverPhaseFailed, metav1.ConditionFalse,
			failType, fmt.Sprintf("Failover failed: %v", err))
	}

	// 8. Success
	eventMsg := fmt.Sprintf("AG %s failed over to %s", fo.Spec.AGName, fo.Spec.TargetReplica)
	if force {
		eventMsg += " (forced, potential data loss)"
	}
	r.Recorder.Event(&fo, corev1.EventTypeNormal, "FailoverCompleted", eventMsg)
	logger.Info("failover completed", "ag", fo.Spec.AGName, "target", fo.Spec.TargetReplica, "force", force)

	// Determine the previous primary by process of elimination
	previousPrimary := "unknown"
	sqlCtx3, cancel3 := sqlContext(ctx)
	defer cancel3()
	agStatus, err := sqlClient.GetAGStatus(sqlCtx3, fo.Spec.AGName)
	if err == nil {
		// After failover, the target is primary, so any secondary that was previously primary
		for _, rs := range agStatus.Replicas {
			if rs.ServerName != fo.Spec.TargetReplica && rs.Role == "SECONDARY" {
				previousPrimary = rs.ServerName
				break
			}
		}
	}

	opmetrics.ReconcileTotal.WithLabelValues("AGFailover", "success").Inc()
	return r.setFailoverCompleted(ctx, &fo, previousPrimary, fo.Spec.TargetReplica, eventMsg)
}

func (r *AGFailoverReconciler) setFailoverStatus(ctx context.Context, fo *v1alpha1.AGFailover,
	phase v1alpha1.FailoverPhase, condStatus metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(fo.DeepCopy())
	fo.Status.Phase = phase
	fo.Status.ObservedGeneration = fo.Generation

	meta.SetStatusCondition(&fo.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: fo.Generation,
	})

	if err := r.Status().Patch(ctx, fo, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AGFailoverReconciler) setFailoverCompleted(ctx context.Context, fo *v1alpha1.AGFailover,
	previousPrimary, newPrimary, message string) (ctrl.Result, error) {

	completionTime := metav1.Now()
	if err := r.Get(ctx, client.ObjectKeyFromObject(fo), fo); err != nil {
		return ctrl.Result{}, err
	}

	patch := client.MergeFrom(fo.DeepCopy())
	fo.Status.Phase = v1alpha1.FailoverPhaseCompleted
	fo.Status.CompletionTime = &completionTime
	fo.Status.PreviousPrimary = previousPrimary
	fo.Status.NewPrimary = newPrimary
	fo.Status.ObservedGeneration = fo.Generation

	meta.SetStatusCondition(&fo.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "FailoverCompleted",
		Message:            message,
		ObservedGeneration: fo.Generation,
	})

	if err := r.Status().Patch(ctx, fo, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AGFailoverReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AGFailover{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](2*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(5), 50)},
			),
		}).
		Complete(r)
}
