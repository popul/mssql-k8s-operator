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

// BackupReconciler reconciles a Backup object.
type BackupReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=backups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=backups/finalizers,verbs=update

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("Backup").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch the Backup CR
	var backup v1alpha1.Backup
	if err := r.Get(ctx, req.NamespacedName, &backup); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. One-shot: skip if already completed or failed
	if backup.Status.Phase == v1alpha1.BackupPhaseCompleted || backup.Status.Phase == v1alpha1.BackupPhaseFailed {
		return ctrl.Result{}, nil
	}

	// 3. Read the credentials Secret
	username, password, err := getCredentialsFromSecret(ctx, r.Client, backup.Namespace, backup.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setBackupStatus(ctx, &backup, v1alpha1.BackupPhaseFailed, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", backup.Spec.Server.CredentialsSecret.Name))
		}
		return r.setBackupStatus(ctx, &backup, v1alpha1.BackupPhaseFailed, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect to SQL Server
	sqlClient, err := connectToSQL(backup.Spec.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&backup, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 5. Transition to Running
	now := metav1.Now()
	if backup.Status.Phase != v1alpha1.BackupPhaseRunning {
		if _, err := r.setBackupStatus(ctx, &backup, v1alpha1.BackupPhaseRunning, metav1.ConditionFalse, "BackupRunning",
			fmt.Sprintf("Backup of %s started", backup.Spec.DatabaseName)); err != nil {
			return ctrl.Result{}, err
		}
		// Re-fetch after status update
		if err := r.Get(ctx, req.NamespacedName, &backup); err != nil {
			return ctrl.Result{}, err
		}
		if backup.Status.StartTime == nil {
			patch := client.MergeFrom(backup.DeepCopy())
			backup.Status.StartTime = &now
			if err := r.Status().Patch(ctx, &backup, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 6. Execute backup
	compression := backup.Spec.Compression != nil && *backup.Spec.Compression
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	if err := sqlClient.BackupDatabase(sqlCtx, backup.Spec.DatabaseName, backup.Spec.Destination,
		string(backup.Spec.Type), compression); err != nil {
		r.Recorder.Event(&backup, corev1.EventTypeWarning, "BackupFailed", err.Error())
		opmetrics.ReconcileErrors.WithLabelValues("Backup", "BackupFailed").Inc()
		return r.setBackupStatus(ctx, &backup, v1alpha1.BackupPhaseFailed, metav1.ConditionFalse, "BackupFailed",
			fmt.Sprintf("Backup failed: %v", err))
	}

	// 7. Success
	r.Recorder.Event(&backup, corev1.EventTypeNormal, "BackupCompleted",
		fmt.Sprintf("Backup of %s completed to %s", backup.Spec.DatabaseName, backup.Spec.Destination))
	logger.Info("backup completed", "database", backup.Spec.DatabaseName, "destination", backup.Spec.Destination)

	// Set completion time and phase
	completionTime := metav1.Now()
	// Re-fetch before final update
	if err := r.Get(ctx, req.NamespacedName, &backup); err != nil {
		return ctrl.Result{}, err
	}
	patch := client.MergeFrom(backup.DeepCopy())
	backup.Status.Phase = v1alpha1.BackupPhaseCompleted
	backup.Status.CompletionTime = &completionTime
	backup.Status.ObservedGeneration = backup.Generation
	meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "BackupCompleted",
		Message:            fmt.Sprintf("Backup of %s completed", backup.Spec.DatabaseName),
		ObservedGeneration: backup.Generation,
	})
	if err := r.Status().Patch(ctx, &backup, patch); err != nil {
		return ctrl.Result{}, err
	}

	opmetrics.ReconcileTotal.WithLabelValues("Backup", "success").Inc()
	return ctrl.Result{}, nil
}

func (r *BackupReconciler) setBackupStatus(ctx context.Context, backup *v1alpha1.Backup,
	phase v1alpha1.BackupPhase, condStatus metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(backup.DeepCopy())
	backup.Status.Phase = phase
	backup.Status.ObservedGeneration = backup.Generation

	meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: backup.Generation,
	})

	if err := r.Status().Patch(ctx, backup, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Backup{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToBackups(context.Background(), mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
