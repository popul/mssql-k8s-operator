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

func newTestAGReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*AvailabilityGroupReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.AvailabilityGroup{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &AvailabilityGroupReconciler{
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

func testAG(name string) *v1alpha1.AvailabilityGroup {
	port := int32(1433)
	dbFailover := true
	backupPref := "Secondary"
	return &v1alpha1.AvailabilityGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.AvailabilityGroupSpec{
			AGName: "myag",
			Replicas: []v1alpha1.AGReplicaSpec{
				{
					ServerName:       "sql-0",
					EndpointURL:      "TCP://sql-0:5022",
					AvailabilityMode: v1alpha1.AvailabilityModeSynchronous,
					FailoverMode:     v1alpha1.FailoverModeAutomatic,
					SeedingMode:      v1alpha1.SeedingModeAutomatic,
					SecondaryRole:    v1alpha1.SecondaryRoleNo,
					Server: v1alpha1.ServerReference{
						Host:              "sql-0",
						Port:              &port,
						CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
					},
				},
				{
					ServerName:       "sql-1",
					EndpointURL:      "TCP://sql-1:5022",
					AvailabilityMode: v1alpha1.AvailabilityModeSynchronous,
					FailoverMode:     v1alpha1.FailoverModeAutomatic,
					SeedingMode:      v1alpha1.SeedingModeAutomatic,
					SecondaryRole:    v1alpha1.SecondaryRoleNo,
					Server: v1alpha1.ServerReference{
						Host:              "sql-1",
						Port:              &port,
						CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
					},
				},
			},
			Databases:                 []v1alpha1.AGDatabaseSpec{{Name: "mydb"}},
			DBFailover:                &dbFailover,
			AutomatedBackupPreference: &backupPref,
		},
	}
}

func getAGCondition(ag *v1alpha1.AvailabilityGroup, condType string) *metav1.Condition {
	for i := range ag.Status.Conditions {
		if ag.Status.Conditions[i].Type == condType {
			return &ag.Status.Conditions[i]
		}
	}
	return nil
}

// reconcileAG runs Reconcile twice: first to add finalizer, second to actually reconcile.
func reconcileAG(r *AvailabilityGroupReconciler, name string) error {
	r.Reconcile(context.Background(), reqFor(name))
	_, err := r.Reconcile(context.Background(), reqFor(name))
	return err
}

// =============================================================================
// CR not found → no error
// =============================================================================

func TestAGReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestAGReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found CR")
	}
}

// =============================================================================
// Creation — AG doesn't exist → CREATE AVAILABILITY GROUP + Ready=True
// =============================================================================

func TestAGReconcile_Creation(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	if err := reconcileAG(r, "test-ag"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("CreateAG") {
		t.Error("expected CreateAG to be called")
	}
	if !mockSQL.WasCalled("JoinAG") {
		t.Error("expected JoinAG to be called for secondary")
	}
	if !mockSQL.WasCalled("GrantAGCreateDatabase") {
		t.Error("expected GrantAGCreateDatabase to be called for automatic seeding")
	}

	var updated v1alpha1.AvailabilityGroup
	r.Client.Get(context.Background(), reqFor("test-ag").NamespacedName, &updated)
	cond := getAGCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True condition")
	}
	if updated.Status.PrimaryReplica != "sql-0" {
		t.Errorf("expected primaryReplica=sql-0, got %s", updated.Status.PrimaryReplica)
	}
}

// =============================================================================
// AG already exists → idempotent, just observes status
// =============================================================================

func TestAGReconcile_AlreadyExists(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-create the AG in mock
	mockSQL.CreateAG(context.Background(), sqlclient.AGConfig{
		Name:                      "myag",
		Replicas:                  []sqlclient.AGReplicaConfig{{ServerName: "sql-0"}, {ServerName: "sql-1"}},
		Databases:                 []string{"mydb"},
		AutomatedBackupPreference: "SECONDARY",
		DBFailover:                true,
	})
	mockSQL.ResetCalls() // reset so we can check what the controller calls

	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	if err := reconcileAG(r, "test-ag"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("CreateAG") {
		t.Error("should not re-create AG when it already exists")
	}
}

// =============================================================================
// Secret not found → Ready=False, SecretNotFound
// =============================================================================

func TestAGReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag}, mockSQL) // no secret

	if err := reconcileAG(r, "test-ag"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated v1alpha1.AvailabilityGroup
	r.Client.Get(context.Background(), reqFor("test-ag").NamespacedName, &updated)
	cond := getAGCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Reason != v1alpha1.ReasonSecretNotFound {
		t.Error("expected SecretNotFound reason")
	}
}

// =============================================================================
// Connection error → transient, returns error for retry
// =============================================================================

func TestAGReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	if err := reconcileAG(r, "test-ag"); err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// =============================================================================
// CreateAG fails → returns error for retry
// =============================================================================

func TestAGReconcile_CreateAGError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("CreateAG", errTest)
	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	if err := reconcileAG(r, "test-ag"); err == nil {
		t.Error("expected error when CreateAG fails")
	}
}

// =============================================================================
// ObservedGeneration is set
// =============================================================================

func TestAGReconcile_ObservedGeneration(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	ag := testAG("test-ag")
	ag.Generation = 5
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	reconcileAG(r, "test-ag")

	var updated v1alpha1.AvailabilityGroup
	r.Client.Get(context.Background(), reqFor("test-ag").NamespacedName, &updated)
	if updated.Status.ObservedGeneration != 5 {
		t.Errorf("expected ObservedGeneration=5, got %d", updated.Status.ObservedGeneration)
	}
}

// =============================================================================
// Requeue with jitter for periodic polling
// =============================================================================

func TestAGReconcile_RequeueWithJitter(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	// First call adds finalizer
	r.Reconcile(context.Background(), reqFor("test-ag"))
	// Second call reconciles
	result, err := r.Reconcile(context.Background(), reqFor("test-ag"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 for periodic polling")
	}
}

// =============================================================================
// Deletion — drops the AG
// =============================================================================

func TestAGReconcile_Deletion(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-create AG
	mockSQL.CreateAG(context.Background(), sqlclient.AGConfig{
		Name:     "myag",
		Replicas: []sqlclient.AGReplicaConfig{{ServerName: "sql-0"}, {ServerName: "sql-1"}},
	})

	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	// Add finalizer first
	reconcileAG(r, "test-ag")

	// Mark for deletion
	var toDelete v1alpha1.AvailabilityGroup
	r.Client.Get(context.Background(), reqFor("test-ag").NamespacedName, &toDelete)
	now := metav1.Now()
	toDelete.DeletionTimestamp = &now
	// Can't set DeletionTimestamp directly via update — simulate by calling handleDeletion
	r.handleDeletion(context.Background(), &toDelete)

	if !mockSQL.WasCalled("DropAG") {
		t.Error("expected DropAG to be called on deletion")
	}
}

// =============================================================================
// HADR endpoint creation
// =============================================================================

func TestAGReconcile_CreatesHADREndpoint(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	ag := testAG("test-ag")
	r, _ := newTestAGReconciler([]runtime.Object{ag, saSecret()}, mockSQL)

	reconcileAG(r, "test-ag")

	if !mockSQL.WasCalled("CreateHADREndpoint") {
		t.Error("expected CreateHADREndpoint to be called")
	}
}
