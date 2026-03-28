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

func newTestRestoreReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*RestoreReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Restore{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &RestoreReconciler{
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

func testRestore(name string) *v1alpha1.Restore {
	port := int32(1433)
	return &v1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.RestoreSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			DatabaseName: "mydb",
			Source:       "/backups/mydb.bak",
		},
	}
}

func getRestoreCondition(restore *v1alpha1.Restore, condType string) *metav1.Condition {
	for i := range restore.Status.Conditions {
		if restore.Status.Conditions[i].Type == condType {
			return &restore.Status.Conditions[i]
		}
	}
	return nil
}

// =============================================================================
// CR not found → no error
// =============================================================================

func TestRestoreReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestRestoreReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found CR")
	}
}

// =============================================================================
// Successful restore → Phase=Completed, Ready=True
// =============================================================================

func TestRestoreReconcile_Success(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	restore := testRestore("test-restore")
	r, _ := newTestRestoreReconciler([]runtime.Object{restore, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-restore"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("RestoreDatabase") {
		t.Error("expected RestoreDatabase to be called")
	}

	var updated v1alpha1.Restore
	r.Client.Get(context.Background(), reqFor("test-restore").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.RestorePhaseCompleted {
		t.Errorf("expected phase=Completed, got %s", updated.Status.Phase)
	}
	cond := getRestoreCondition(&updated, v1alpha1.ConditionReady)
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

func TestRestoreReconcile_AlreadyCompleted(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	restore := testRestore("test-restore")
	restore.Status.Phase = v1alpha1.RestorePhaseCompleted
	r, _ := newTestRestoreReconciler([]runtime.Object{restore, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-restore"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockSQL.WasCalled("RestoreDatabase") {
		t.Error("should not re-execute restore when already completed")
	}
}

// =============================================================================
// Already failed → no-op
// =============================================================================

func TestRestoreReconcile_AlreadyFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	restore := testRestore("test-restore")
	restore.Status.Phase = v1alpha1.RestorePhaseFailed
	r, _ := newTestRestoreReconciler([]runtime.Object{restore, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-restore"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockSQL.WasCalled("RestoreDatabase") {
		t.Error("should not re-execute restore when already failed")
	}
}

// =============================================================================
// Secret not found → Phase=Failed
// =============================================================================

func TestRestoreReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	restore := testRestore("test-restore")
	r, _ := newTestRestoreReconciler([]runtime.Object{restore}, mockSQL) // no secret

	_, err := r.Reconcile(context.Background(), reqFor("test-restore"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.Restore
	r.Client.Get(context.Background(), reqFor("test-restore").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.RestorePhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}
	cond := getRestoreCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Reason != v1alpha1.ReasonSecretNotFound {
		t.Error("expected SecretNotFound reason")
	}
}

// =============================================================================
// Connection error → transient, returns error for retry
// =============================================================================

func TestRestoreReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	restore := testRestore("test-restore")
	r, _ := newTestRestoreReconciler([]runtime.Object{restore, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-restore"))
	if err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// =============================================================================
// Restore SQL error → Phase=Failed
// =============================================================================

func TestRestoreReconcile_RestoreError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("RestoreDatabase", errTest)
	restore := testRestore("test-restore")
	r, _ := newTestRestoreReconciler([]runtime.Object{restore, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-restore"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.Restore
	r.Client.Get(context.Background(), reqFor("test-restore").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.RestorePhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}
}

// =============================================================================
// ObservedGeneration is set
// =============================================================================

func TestRestoreReconcile_ObservedGeneration(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	restore := testRestore("test-restore")
	restore.Generation = 3
	r, _ := newTestRestoreReconciler([]runtime.Object{restore, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("test-restore"))

	var updated v1alpha1.Restore
	r.Client.Get(context.Background(), reqFor("test-restore").NamespacedName, &updated)
	if updated.Status.ObservedGeneration != 3 {
		t.Errorf("expected ObservedGeneration=3, got %d", updated.Status.ObservedGeneration)
	}
}

// =============================================================================
// No requeue after completion (one-shot)
// =============================================================================

func TestRestoreReconcile_NoRequeueAfterCompletion(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	restore := testRestore("test-restore")
	r, _ := newTestRestoreReconciler([]runtime.Object{restore, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("test-restore"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Error("expected no requeue after successful one-shot restore")
	}
}
