package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

func newTestBackupReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*BackupReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Backup{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &BackupReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: recorder,
		SQLClientFactory: func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
			if mockSQL.ConnectError != nil {
				return nil, mockSQL.ConnectError
			}
			return mockSQL, nil
		},
	}
	return r, recorder
}

func testBackup(name string) *v1alpha1.Backup {
	port := int32(1433)
	compression := false
	return &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.BackupSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			DatabaseName: "mydb",
			Type:         v1alpha1.BackupTypeFull,
			Destination:  "/backups/mydb.bak",
			Compression:  &compression,
		},
	}
}

func getBackupCondition(backup *v1alpha1.Backup, condType string) *metav1.Condition {
	for i := range backup.Status.Conditions {
		if backup.Status.Conditions[i].Type == condType {
			return &backup.Status.Conditions[i]
		}
	}
	return nil
}

// =============================================================================
// CR not found → no error
// =============================================================================

func TestBackupReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestBackupReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found CR")
	}
}

// =============================================================================
// Successful backup → Phase=Completed, Ready=True
// =============================================================================

func TestBackupReconcile_Success(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	backup := testBackup("test-backup")
	r, _ := newTestBackupReconciler([]runtime.Object{backup, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-backup"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("BackupDatabase") {
		t.Error("expected BackupDatabase to be called")
	}

	var updated v1alpha1.Backup
	r.Client.Get(context.Background(), reqFor("test-backup").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.BackupPhaseCompleted {
		t.Errorf("expected phase=Completed, got %s", updated.Status.Phase)
	}
	cond := getBackupCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True condition")
	}
	if updated.Status.CompletionTime == nil {
		t.Error("expected CompletionTime to be set")
	}
}

// =============================================================================
// Already completed → no-op
// =============================================================================

func TestBackupReconcile_AlreadyCompleted(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	backup := testBackup("test-backup")
	backup.Status.Phase = v1alpha1.BackupPhaseCompleted
	r, _ := newTestBackupReconciler([]runtime.Object{backup, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-backup"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockSQL.WasCalled("BackupDatabase") {
		t.Error("should not re-execute backup when already completed")
	}
}

// =============================================================================
// Already failed → no-op
// =============================================================================

func TestBackupReconcile_AlreadyFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	backup := testBackup("test-backup")
	backup.Status.Phase = v1alpha1.BackupPhaseFailed
	r, _ := newTestBackupReconciler([]runtime.Object{backup, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-backup"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockSQL.WasCalled("BackupDatabase") {
		t.Error("should not re-execute backup when already failed")
	}
}

// =============================================================================
// Secret not found → Phase=Failed
// =============================================================================

func TestBackupReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	backup := testBackup("test-backup")
	r, _ := newTestBackupReconciler([]runtime.Object{backup}, mockSQL) // no secret

	_, err := r.Reconcile(context.Background(), reqFor("test-backup"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.Backup
	r.Client.Get(context.Background(), reqFor("test-backup").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.BackupPhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}
	cond := getBackupCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Reason != v1alpha1.ReasonSecretNotFound {
		t.Error("expected SecretNotFound reason")
	}
}

// =============================================================================
// Connection error → transient, returns error for retry
// =============================================================================

func TestBackupReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	backup := testBackup("test-backup")
	r, _ := newTestBackupReconciler([]runtime.Object{backup, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-backup"))
	if err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// =============================================================================
// Backup SQL error → Phase=Failed
// =============================================================================

func TestBackupReconcile_BackupError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("BackupDatabase", errTest)
	backup := testBackup("test-backup")
	r, _ := newTestBackupReconciler([]runtime.Object{backup, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-backup"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.Backup
	r.Client.Get(context.Background(), reqFor("test-backup").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.BackupPhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}
}

// =============================================================================
// ObservedGeneration is set
// =============================================================================

func TestBackupReconcile_ObservedGeneration(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	backup := testBackup("test-backup")
	backup.Generation = 3
	r, _ := newTestBackupReconciler([]runtime.Object{backup, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("test-backup"))

	var updated v1alpha1.Backup
	r.Client.Get(context.Background(), reqFor("test-backup").NamespacedName, &updated)
	if updated.Status.ObservedGeneration != 3 {
		t.Errorf("expected ObservedGeneration=3, got %d", updated.Status.ObservedGeneration)
	}
}

// =============================================================================
// No requeue after completion (one-shot)
// =============================================================================

func TestBackupReconcile_NoRequeueAfterCompletion(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	backup := testBackup("test-backup")
	r, _ := newTestBackupReconciler([]runtime.Object{backup, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("test-backup"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Error("expected no requeue after successful one-shot backup")
	}
}
