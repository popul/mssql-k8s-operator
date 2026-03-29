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

func newTestAGFailoverReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*AGFailoverReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.AGFailover{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &AGFailoverReconciler{
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

func testAGFailover(name string, force bool) *v1alpha1.AGFailover {
	port := int32(1433)
	return &v1alpha1.AGFailover{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.AGFailoverSpec{
			AGName:        "myag",
			TargetReplica: "sql-1",
			Force:         &force,
			Server: v1alpha1.ServerReference{
				Host:              "sql-1.sql-headless",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
		},
	}
}

func getFailoverCondition(fo *v1alpha1.AGFailover, condType string) *metav1.Condition {
	for i := range fo.Status.Conditions {
		if fo.Status.Conditions[i].Type == condType {
			return &fo.Status.Conditions[i]
		}
	}
	return nil
}

// setupMockAG creates an AG with sql-0 as primary and sql-1 as secondary.
func setupMockAG(mockSQL *sqlclient.MockClient) {
	mockSQL.CreateAG(context.Background(), &sqlclient.AGConfig{
		Name:                      "myag",
		Replicas:                  []sqlclient.AGReplicaConfig{{ServerName: "sql-0"}, {ServerName: "sql-1"}},
		AutomatedBackupPreference: "SECONDARY",
		DBFailover:                true,
	})
}

// =============================================================================
// CR not found → no error
// =============================================================================

func TestAGFailoverReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestAGFailoverReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found CR")
	}
}

// =============================================================================
// Successful failover → Phase=Completed, Ready=True
// =============================================================================

func TestAGFailoverReconcile_Success(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	setupMockAG(mockSQL)

	fo := testAGFailover("test-failover", false)
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("FailoverAG") {
		t.Error("expected FailoverAG to be called")
	}
	if mockSQL.WasCalled("ForceFailoverAG") {
		t.Error("should not call ForceFailoverAG when force=false")
	}

	var updated v1alpha1.AGFailover
	r.Client.Get(context.Background(), reqFor("test-failover").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.FailoverPhaseCompleted {
		t.Errorf("expected phase=Completed, got %s", updated.Status.Phase)
	}
	cond := getFailoverCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True condition")
	}
	if updated.Status.NewPrimary != "sql-1" {
		t.Errorf("expected newPrimary=sql-1, got %s", updated.Status.NewPrimary)
	}
	if updated.Status.CompletionTime == nil {
		t.Error("expected CompletionTime to be set")
	}
}

// =============================================================================
// Force failover → calls ForceFailoverAG
// =============================================================================

func TestAGFailoverReconcile_ForceFailover(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	setupMockAG(mockSQL)

	fo := testAGFailover("test-failover", true)
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("ForceFailoverAG") {
		t.Error("expected ForceFailoverAG to be called")
	}
	if mockSQL.WasCalled("FailoverAG") {
		t.Error("should not call FailoverAG when force=true")
	}
}

// =============================================================================
// Target already primary → Completed (no-op)
// =============================================================================

func TestAGFailoverReconcile_TargetAlreadyPrimary(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	setupMockAG(mockSQL)

	// Target sql-0 which is already PRIMARY
	fo := testAGFailover("test-failover", false)
	fo.Spec.TargetReplica = "sql-0"
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("FailoverAG") {
		t.Error("should not call FailoverAG when target is already primary")
	}

	var updated v1alpha1.AGFailover
	r.Client.Get(context.Background(), reqFor("test-failover").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.FailoverPhaseCompleted {
		t.Errorf("expected phase=Completed, got %s", updated.Status.Phase)
	}
}

// =============================================================================
// Already completed → no-op
// =============================================================================

func TestAGFailoverReconcile_AlreadyCompleted(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	fo := testAGFailover("test-failover", false)
	fo.Status.Phase = v1alpha1.FailoverPhaseCompleted
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockSQL.WasCalled("FailoverAG") {
		t.Error("should not re-execute failover when already completed")
	}
}

// =============================================================================
// Already failed → no-op
// =============================================================================

func TestAGFailoverReconcile_AlreadyFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	fo := testAGFailover("test-failover", false)
	fo.Status.Phase = v1alpha1.FailoverPhaseFailed
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockSQL.WasCalled("FailoverAG") {
		t.Error("should not re-execute failover when already failed")
	}
}

// =============================================================================
// Secret not found → Phase=Failed
// =============================================================================

func TestAGFailoverReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	fo := testAGFailover("test-failover", false)
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.AGFailover
	r.Client.Get(context.Background(), reqFor("test-failover").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.FailoverPhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}
}

// =============================================================================
// Connection error → transient, returns error for retry
// =============================================================================

func TestAGFailoverReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	fo := testAGFailover("test-failover", false)
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// =============================================================================
// FailoverAG SQL error → Phase=Failed
// =============================================================================

func TestAGFailoverReconcile_FailoverError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	setupMockAG(mockSQL)
	mockSQL.SetMethodError("FailoverAG", errTest)

	fo := testAGFailover("test-failover", false)
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.AGFailover
	r.Client.Get(context.Background(), reqFor("test-failover").NamespacedName, &updated)
	if updated.Status.Phase != v1alpha1.FailoverPhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}
}

// =============================================================================
// No requeue after completion (one-shot)
// =============================================================================

func TestAGFailoverReconcile_NoRequeueAfterCompletion(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	setupMockAG(mockSQL)

	fo := testAGFailover("test-failover", false)
	r, _ := newTestAGFailoverReconciler([]runtime.Object{fo, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("test-failover"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Error("expected no requeue after successful one-shot failover")
	}
}
