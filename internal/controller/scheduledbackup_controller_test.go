package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

func newTestScheduledBackupReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient, now time.Time) (*ScheduledBackupReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	clientBuilder := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.ScheduledBackup{}, &v1alpha1.Backup{})
	for _, obj := range objs {
		clientBuilder = clientBuilder.WithRuntimeObjects(obj)
	}
	k8sClient := clientBuilder.Build()
	recorder := record.NewFakeRecorder(20)

	r := &ScheduledBackupReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: recorder,
		Now:      func() time.Time { return now },
		SQLClientFactory: func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
			return mockSQL, nil
		},
	}
	return r, recorder
}

func testScheduledBackup(name string) *v1alpha1.ScheduledBackup {
	port := int32(1433)
	return &v1alpha1.ScheduledBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			Generation:        1,
			CreationTimestamp: metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
		Spec: v1alpha1.ScheduledBackupSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			DatabaseName:        "myapp",
			Schedule:            "0 2 * * *", // daily at 2 AM
			Type:                v1alpha1.BackupTypeFull,
			DestinationTemplate: "/backups/{{.DatabaseName}}-{{.Timestamp}}.bak",
		},
	}
}

func TestScheduledBackup_CreatesBackupWhenDue(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	sb := testScheduledBackup("nightly")
	// Set now to 2 AM so schedule triggers
	now := time.Date(2024, 1, 2, 2, 0, 1, 0, time.UTC)
	r, _ := newTestScheduledBackupReconciler([]runtime.Object{sb, saSecret()}, mockSQL, now)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nightly", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify a Backup CR was created
	var backupList v1alpha1.BackupList
	if err := r.List(context.Background(), &backupList); err != nil {
		t.Fatalf("failed to list backups: %v", err)
	}
	if len(backupList.Items) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backupList.Items))
	}
	bak := backupList.Items[0]
	if bak.Spec.DatabaseName != "myapp" {
		t.Errorf("expected database=myapp, got %s", bak.Spec.DatabaseName)
	}
	if bak.Spec.Destination == "" {
		t.Error("expected destination to be rendered")
	}
	if bak.Labels["mssql.popul.io/scheduled-backup"] != "nightly" {
		t.Error("expected label pointing back to ScheduledBackup")
	}

	// Verify status updated
	var updated v1alpha1.ScheduledBackup
	_ = r.Get(context.Background(), types.NamespacedName{Name: "nightly", Namespace: "default"}, &updated)
	if updated.Status.ActiveBackup == "" {
		t.Error("expected ActiveBackup to be set")
	}
	if updated.Status.TotalBackups != 1 {
		t.Errorf("expected TotalBackups=1, got %d", updated.Status.TotalBackups)
	}
}

func TestScheduledBackup_NotDueYet(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	sb := testScheduledBackup("nightly")
	// Set LastScheduleTime so next schedule is Jan 3 02:00
	lastSched := metav1.NewTime(time.Date(2024, 1, 2, 2, 0, 0, 0, time.UTC))
	sb.Status.LastScheduleTime = &lastSched
	// Now is Jan 2 23:00 (before Jan 3 02:00)
	now := time.Date(2024, 1, 2, 23, 0, 0, 0, time.UTC)
	r, _ := newTestScheduledBackupReconciler([]runtime.Object{sb, saSecret()}, mockSQL, now)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nightly", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue until the scheduled time
	if result.RequeueAfter <= 0 {
		t.Error("expected RequeueAfter > 0")
	}

	// No backup should have been created
	var backupList v1alpha1.BackupList
	_ = r.List(context.Background(), &backupList)
	if len(backupList.Items) != 0 {
		t.Errorf("expected 0 backups, got %d", len(backupList.Items))
	}
}

func TestScheduledBackup_Suspended(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	sb := testScheduledBackup("nightly")
	suspended := true
	sb.Spec.Suspend = &suspended
	now := time.Date(2024, 1, 2, 2, 0, 1, 0, time.UTC)
	r, _ := newTestScheduledBackupReconciler([]runtime.Object{sb, saSecret()}, mockSQL, now)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nightly", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No backup should be created even though it's due
	var backupList v1alpha1.BackupList
	_ = r.List(context.Background(), &backupList)
	if len(backupList.Items) != 0 {
		t.Errorf("expected 0 backups when suspended, got %d", len(backupList.Items))
	}
}

func TestScheduledBackup_ActiveBackupCompleted(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	sb := testScheduledBackup("nightly")
	sb.Status.ActiveBackup = "nightly-20240102-020000"
	sb.Status.TotalBackups = 1

	completionTime := metav1.NewTime(time.Date(2024, 1, 2, 2, 5, 0, 0, time.UTC))
	bak := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-20240102-020000",
			Namespace: "default",
			Labels:    map[string]string{"mssql.popul.io/scheduled-backup": "nightly"},
		},
		Spec: v1alpha1.BackupSpec{
			DatabaseName: "myapp",
			Destination:  "/backups/myapp-20240102-020000.bak",
		},
		Status: v1alpha1.BackupStatus{
			Phase:          v1alpha1.BackupPhaseCompleted,
			CompletionTime: &completionTime,
		},
	}

	now := time.Date(2024, 1, 2, 2, 6, 0, 0, time.UTC)
	r, _ := newTestScheduledBackupReconciler([]runtime.Object{sb, bak, saSecret()}, mockSQL, now)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nightly", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.ScheduledBackup
	_ = r.Get(context.Background(), types.NamespacedName{Name: "nightly", Namespace: "default"}, &updated)
	if updated.Status.ActiveBackup != "" {
		t.Error("expected ActiveBackup to be cleared after completion")
	}
	if updated.Status.LastSuccessfulBackup != "nightly-20240102-020000" {
		t.Errorf("expected LastSuccessfulBackup to be set, got %s", updated.Status.LastSuccessfulBackup)
	}
	if updated.Status.SuccessfulBackups != 1 {
		t.Errorf("expected SuccessfulBackups=1, got %d", updated.Status.SuccessfulBackups)
	}
	if len(updated.Status.History) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(updated.Status.History))
	}
}

func TestScheduledBackup_ActiveBackupFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	sb := testScheduledBackup("nightly")
	sb.Status.ActiveBackup = "nightly-20240102-020000"

	bak := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-20240102-020000",
			Namespace: "default",
			Labels:    map[string]string{"mssql.popul.io/scheduled-backup": "nightly"},
		},
		Spec: v1alpha1.BackupSpec{DatabaseName: "myapp"},
		Status: v1alpha1.BackupStatus{
			Phase: v1alpha1.BackupPhaseFailed,
		},
	}

	now := time.Date(2024, 1, 2, 2, 6, 0, 0, time.UTC)
	r, _ := newTestScheduledBackupReconciler([]runtime.Object{sb, bak, saSecret()}, mockSQL, now)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nightly", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.ScheduledBackup
	_ = r.Get(context.Background(), types.NamespacedName{Name: "nightly", Namespace: "default"}, &updated)
	if updated.Status.FailedBackups != 1 {
		t.Errorf("expected FailedBackups=1, got %d", updated.Status.FailedBackups)
	}
}

func TestScheduledBackup_Retention_MaxCount(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	sb := testScheduledBackup("nightly")
	maxCount := int32(2)
	sb.Spec.Retention = &v1alpha1.RetentionPolicy{MaxCount: &maxCount}
	sb.Status.ActiveBackup = "nightly-latest"
	sb.Status.TotalBackups = 4

	// Create 3 completed backups + 1 active
	bak1 := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-old1", Namespace: "default",
			Labels:            map[string]string{"mssql.popul.io/scheduled-backup": "nightly"},
			CreationTimestamp: metav1.NewTime(time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC)),
		},
		Status: v1alpha1.BackupStatus{Phase: v1alpha1.BackupPhaseCompleted},
	}
	bak2 := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-old2", Namespace: "default",
			Labels:            map[string]string{"mssql.popul.io/scheduled-backup": "nightly"},
			CreationTimestamp: metav1.NewTime(time.Date(2024, 1, 2, 2, 0, 0, 0, time.UTC)),
		},
		Status: v1alpha1.BackupStatus{Phase: v1alpha1.BackupPhaseCompleted},
	}
	bak3 := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-old3", Namespace: "default",
			Labels:            map[string]string{"mssql.popul.io/scheduled-backup": "nightly"},
			CreationTimestamp: metav1.NewTime(time.Date(2024, 1, 3, 2, 0, 0, 0, time.UTC)),
		},
		Status: v1alpha1.BackupStatus{Phase: v1alpha1.BackupPhaseCompleted},
	}
	bakLatest := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-latest", Namespace: "default",
			Labels: map[string]string{"mssql.popul.io/scheduled-backup": "nightly"},
		},
		Status: v1alpha1.BackupStatus{Phase: v1alpha1.BackupPhaseCompleted},
	}

	now := time.Date(2024, 1, 4, 2, 1, 0, 0, time.UTC)
	r, _ := newTestScheduledBackupReconciler([]runtime.Object{sb, bak1, bak2, bak3, bakLatest, saSecret()}, mockSQL, now)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nightly", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After retention cleanup with maxCount=2, the oldest 2 should be deleted
	var backupList v1alpha1.BackupList
	_ = r.List(context.Background(), &backupList)
	remaining := 0
	for _, b := range backupList.Items {
		if b.Status.Phase == v1alpha1.BackupPhaseCompleted {
			remaining++
		}
	}
	// We expect 2 completed backups (maxCount=2)
	if remaining > 2 {
		t.Errorf("expected at most 2 completed backups after retention, got %d", remaining)
	}
}

func TestScheduledBackup_DestinationTemplate(t *testing.T) {
	r := &ScheduledBackupReconciler{}
	now := time.Date(2024, 6, 15, 14, 30, 45, 0, time.UTC)

	result, err := r.renderDestination("/backups/{{.DatabaseName}}-{{.Timestamp}}-{{.Type}}.bak", "mydb", now, "Full")
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	expected := "/backups/mydb-20240615-143045-Full.bak"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestScheduledBackup_InvalidSchedule(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	sb := testScheduledBackup("bad-cron")
	sb.Spec.Schedule = "not-a-cron"
	now := time.Date(2024, 1, 2, 2, 0, 1, 0, time.UTC)
	r, _ := newTestScheduledBackupReconciler([]runtime.Object{sb, saSecret()}, mockSQL, now)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-cron", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.ScheduledBackup
	_ = r.Get(context.Background(), types.NamespacedName{Name: "bad-cron", Namespace: "default"}, &updated)
	if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Reason != "InvalidSchedule" {
		t.Error("expected InvalidSchedule condition")
	}
}
