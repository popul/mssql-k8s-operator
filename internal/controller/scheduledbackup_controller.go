package controller

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"text/template"
	"time"

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	"golang.org/x/time/rate"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

const maxHistoryLen = 10

// ScheduledBackupReconciler reconciles a ScheduledBackup object.
type ScheduledBackupReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
	Now              func() time.Time // injectable for testing
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=scheduledbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=scheduledbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=scheduledbackups/finalizers,verbs=update

func (r *ScheduledBackupReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ScheduledBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("ScheduledBackup").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch the ScheduledBackup CR
	var sb v1alpha1.ScheduledBackup
	if err := r.Get(ctx, req.NamespacedName, &sb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Parse schedule
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(sb.Spec.Schedule)
	if err != nil {
		return r.setCondition(ctx, &sb, metav1.ConditionFalse, "InvalidSchedule",
			fmt.Sprintf("invalid cron expression: %v", err))
	}

	// 3. Check if suspended
	if sb.Spec.Suspend != nil && *sb.Spec.Suspend {
		return r.setCondition(ctx, &sb, metav1.ConditionTrue, "Suspended", "Schedule is suspended")
	}

	// 4. Check active backup if any
	if sb.Status.ActiveBackup != "" {
		var activeBak v1alpha1.Backup
		if err := r.Get(ctx, types.NamespacedName{Name: sb.Status.ActiveBackup, Namespace: sb.Namespace}, &activeBak); err != nil {
			if apierrors.IsNotFound(err) {
				// Active backup was deleted externally
				return r.clearActiveBackup(ctx, &sb)
			}
			return ctrl.Result{}, err
		}

		switch activeBak.Status.Phase {
		case v1alpha1.BackupPhaseCompleted:
			return r.onBackupCompleted(ctx, &sb, &activeBak)
		case v1alpha1.BackupPhaseFailed:
			return r.onBackupFailed(ctx, &sb, &activeBak)
		default:
			// Still running — requeue to check later
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// 5. Compute next schedule time
	now := r.now()
	var lastSchedule time.Time
	if sb.Status.LastScheduleTime != nil {
		lastSchedule = sb.Status.LastScheduleTime.Time
	} else {
		lastSchedule = sb.CreationTimestamp.Time
	}

	nextTime := sched.Next(lastSchedule)

	// Update NextScheduleTime
	nextMeta := metav1.NewTime(nextTime)
	if sb.Status.NextScheduleTime == nil || !sb.Status.NextScheduleTime.Equal(&nextMeta) {
		patch := client.MergeFrom(sb.DeepCopy())
		sb.Status.NextScheduleTime = &nextMeta
		_ = r.Status().Patch(ctx, &sb, patch)
	}

	// 6. If it's time, create a Backup CR
	if now.Before(nextTime) {
		requeueIn := nextTime.Sub(now) + time.Second // +1s buffer
		return ctrl.Result{RequeueAfter: requeueIn}, nil
	}

	// Create the backup
	destination, err := r.renderDestination(sb.Spec.DestinationTemplate, sb.Spec.DatabaseName, now, string(sb.Spec.Type))
	if err != nil {
		return r.setCondition(ctx, &sb, metav1.ConditionFalse, "TemplateError",
			fmt.Sprintf("failed to render destination template: %v", err))
	}

	backupName := fmt.Sprintf("%s-%s", sb.Name, now.Format("20060102-150405"))
	backup := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupName,
			Namespace: sb.Namespace,
			Labels: map[string]string{
				"mssql.popul.io/scheduled-backup": sb.Name,
			},
		},
		Spec: v1alpha1.BackupSpec{
			Server:       sb.Spec.Server,
			DatabaseName: sb.Spec.DatabaseName,
			Type:         sb.Spec.Type,
			Destination:  destination,
			Compression:  sb.Spec.Compression,
		},
	}

	if err := controllerutil.SetControllerReference(&sb, backup, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set owner reference: %w", err)
	}

	if err := r.Create(ctx, backup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create Backup CR: %w", err)
	}

	r.Recorder.Eventf(&sb, "Normal", "BackupCreated",
		"Created Backup %s for database %s", backupName, sb.Spec.DatabaseName)
	logger.Info("created scheduled backup", "backup", backupName)

	// Update status
	nowMeta := metav1.NewTime(now)
	patch := client.MergeFrom(sb.DeepCopy())
	sb.Status.ActiveBackup = backupName
	sb.Status.LastScheduleTime = &nowMeta
	sb.Status.TotalBackups++
	sb.Status.ObservedGeneration = sb.Generation
	if err := r.Status().Patch(ctx, &sb, patch); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to monitor the active backup
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *ScheduledBackupReconciler) onBackupCompleted(ctx context.Context, sb *v1alpha1.ScheduledBackup, bak *v1alpha1.Backup) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	patch := client.MergeFrom(sb.DeepCopy())
	sb.Status.ActiveBackup = ""
	sb.Status.LastSuccessfulBackup = bak.Name
	sb.Status.SuccessfulBackups++

	// Add to history
	entry := v1alpha1.ScheduledBackupHistory{
		BackupName:     bak.Name,
		StartTime:      bak.Status.StartTime,
		CompletionTime: bak.Status.CompletionTime,
		Phase:          bak.Status.Phase,
		Destination:    bak.Spec.Destination,
	}
	sb.Status.History = append(sb.Status.History, entry)
	if len(sb.Status.History) > maxHistoryLen {
		sb.Status.History = sb.Status.History[len(sb.Status.History)-maxHistoryLen:]
	}

	meta.SetStatusCondition(&sb.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "BackupCompleted",
		Message:            fmt.Sprintf("Last backup %s completed successfully", bak.Name),
		ObservedGeneration: sb.Generation,
	})

	if err := r.Status().Patch(ctx, sb, patch); err != nil {
		return ctrl.Result{}, err
	}

	// Cleanup old backups according to retention policy
	if err := r.cleanupRetention(ctx, sb); err != nil {
		logger.Error(err, "failed to cleanup old backups")
	}

	opmetrics.ReconcileTotal.WithLabelValues("ScheduledBackup", "success").Inc()
	// Requeue to wait for next schedule
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *ScheduledBackupReconciler) onBackupFailed(ctx context.Context, sb *v1alpha1.ScheduledBackup, bak *v1alpha1.Backup) (ctrl.Result, error) {
	patch := client.MergeFrom(sb.DeepCopy())
	sb.Status.ActiveBackup = ""
	sb.Status.FailedBackups++

	entry := v1alpha1.ScheduledBackupHistory{
		BackupName:     bak.Name,
		StartTime:      bak.Status.StartTime,
		CompletionTime: bak.Status.CompletionTime,
		Phase:          bak.Status.Phase,
		Destination:    bak.Spec.Destination,
	}
	sb.Status.History = append(sb.Status.History, entry)
	if len(sb.Status.History) > maxHistoryLen {
		sb.Status.History = sb.Status.History[len(sb.Status.History)-maxHistoryLen:]
	}

	meta.SetStatusCondition(&sb.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "BackupFailed",
		Message:            fmt.Sprintf("Last backup %s failed", bak.Name),
		ObservedGeneration: sb.Generation,
	})

	if err := r.Status().Patch(ctx, sb, patch); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(sb, "Warning", "BackupFailed",
		"Scheduled backup %s failed", bak.Name)
	opmetrics.ReconcileErrors.WithLabelValues("ScheduledBackup", "BackupFailed").Inc()

	// Continue on next schedule
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *ScheduledBackupReconciler) clearActiveBackup(ctx context.Context, sb *v1alpha1.ScheduledBackup) (ctrl.Result, error) {
	patch := client.MergeFrom(sb.DeepCopy())
	sb.Status.ActiveBackup = ""
	if err := r.Status().Patch(ctx, sb, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *ScheduledBackupReconciler) cleanupRetention(ctx context.Context, sb *v1alpha1.ScheduledBackup) error {
	if sb.Spec.Retention == nil {
		return nil
	}

	// List all backups owned by this scheduled backup
	var backupList v1alpha1.BackupList
	if err := r.List(ctx, &backupList, client.InNamespace(sb.Namespace),
		client.MatchingLabels{"mssql.popul.io/scheduled-backup": sb.Name}); err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	// Filter completed backups and sort by creation time
	var completed []v1alpha1.Backup
	for _, b := range backupList.Items {
		if b.Status.Phase == v1alpha1.BackupPhaseCompleted {
			completed = append(completed, b)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].CreationTimestamp.Before(&completed[j].CreationTimestamp)
	})

	now := r.now()
	var toDelete []v1alpha1.Backup

	// MaxAge-based cleanup
	if sb.Spec.Retention.MaxAge != nil {
		maxAge, err := time.ParseDuration(*sb.Spec.Retention.MaxAge)
		if err == nil {
			for _, b := range completed {
				if now.Sub(b.CreationTimestamp.Time) > maxAge {
					toDelete = append(toDelete, b)
				}
			}
		}
	}

	// MaxCount-based cleanup
	if sb.Spec.Retention.MaxCount != nil && *sb.Spec.Retention.MaxCount > 0 {
		maxCount := int(*sb.Spec.Retention.MaxCount)
		if len(completed) > maxCount {
			excess := completed[:len(completed)-maxCount]
			for _, b := range excess {
				found := false
				for _, d := range toDelete {
					if d.Name == b.Name {
						found = true
						break
					}
				}
				if !found {
					toDelete = append(toDelete, b)
				}
			}
		}
	}

	for i := range toDelete {
		if err := r.Delete(ctx, &toDelete[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete old backup %s: %w", toDelete[i].Name, err)
		}
	}

	return nil
}

func (r *ScheduledBackupReconciler) renderDestination(tmpl, dbName string, t time.Time, backupType string) (string, error) {
	data := struct {
		DatabaseName string
		Timestamp    string
		Type         string
	}{
		DatabaseName: dbName,
		Timestamp:    t.Format("20060102-150405"),
		Type:         backupType,
	}

	parsedTmpl, err := template.New("dest").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := parsedTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func mapBackupToScheduledBackup(ctx context.Context, c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		labels := obj.GetLabels()
		sbName, ok := labels["mssql.popul.io/scheduled-backup"]
		if !ok {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Name:      sbName,
				Namespace: obj.GetNamespace(),
			},
		}}
	}
}

func (r *ScheduledBackupReconciler) setCondition(ctx context.Context, sb *v1alpha1.ScheduledBackup,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(sb.DeepCopy())
	meta.SetStatusCondition(&sb.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: sb.Generation,
	})
	sb.Status.ObservedGeneration = sb.Generation

	if err := r.Status().Patch(ctx, sb, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ScheduledBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ScheduledBackup{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&v1alpha1.Backup{}).
		Watches(&v1alpha1.Backup{}, handler.EnqueueRequestsFromMapFunc(mapBackupToScheduledBackup(context.Background(), mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 3,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
