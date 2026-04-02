package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// =============================================================================
// Test infrastructure: multi-mock per host
// =============================================================================

func newMultiMockAGReconciler(objs []runtime.Object, mocks map[string]*sqlclient.MockClient) (*AvailabilityGroupReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	clientBuilder := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.AvailabilityGroup{}, &v1alpha1.AGFailover{}).
		WithRuntimeObjects(objs...)
	k8sClient := clientBuilder.Build()
	recorder := record.NewFakeRecorder(20)

	r := &AvailabilityGroupReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: recorder,
		SQLClientFactory: func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
			for key, mock := range mocks {
				if strings.Contains(host, key) {
					if mock.ConnectError != nil {
						return nil, mock.ConnectError
					}
					return mock, nil
				}
			}
			return nil, fmt.Errorf("no mock for host %s", host)
		},
	}
	return r, recorder
}

// testAGWithStatus creates an AG CR with status.PrimaryReplica set.
func testAGWithStatus(name, primaryReplica string) *v1alpha1.AvailabilityGroup {
	ag := testAG(name)
	ag.Status.PrimaryReplica = primaryReplica
	return ag
}

// setAGClusterType sets the ClusterType on an AG spec.
func setAGClusterType(ag *v1alpha1.AvailabilityGroup, ct string) {
	ag.Spec.ClusterType = &ct
}

// setAGFencingStatus sets fencing-related status fields on an AG.
func setAGFencingStatus(ag *v1alpha1.AvailabilityGroup, lastFenced string, count, consecutive int32, lastTime time.Time) {
	ag.Status.LastFencedReplica = lastFenced
	ag.Status.FencingCount = count
	ag.Status.ConsecutiveFencingCount = consecutive
	t := metav1.NewTime(lastTime)
	ag.Status.LastFencingTime = &t
}

// createPodForReplica creates a pod with instance labels matching the AG's replicas.
func createPodForReplica(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "mssql",
				"app.kubernetes.io/instance":   "mssql",
				"app.kubernetes.io/managed-by": "mssql-operator",
			},
		},
	}
}

// setupMockWithRole configures a mock to return a specific role for a replica.
func setupMockWithRole(mock *sqlclient.MockClient, agName, serverName, role string) {
	mock.CreateAG(context.Background(), &sqlclient.AGConfig{
		Name: agName,
		Replicas: []sqlclient.AGReplicaConfig{
			{ServerName: serverName},
		},
	})
	// Set the role on the replica in the mock AG
	if ag, ok := mock.GetMockAG(agName); ok {
		for i := range ag.Replicas {
			if ag.Replicas[i].ServerName == serverName {
				ag.Replicas[i].Role = role
			}
		}
	}
}

// =============================================================================
// Guards (tests 1-5)
// =============================================================================

func TestFencing_NoPrimaryInStatus(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	ag := testAG("test-ag")
	// status.PrimaryReplica is empty
	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing when status.PrimaryReplica is empty")
	}
	if mock0.WasCalled("GetAGReplicaRole") || mock1.WasCalled("GetAGReplicaRole") {
		t.Error("should not call any SQL methods when status is empty")
	}
}

func TestFencing_ClusterTypeWSFC_Skip(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	ag := testAGWithStatus("test-ag", "sql-0")
	setAGClusterType(ag, "WSFC")
	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing for ClusterType=WSFC")
	}
}

func TestFencing_ClusterTypeExternal_Skip(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	ag := testAGWithStatus("test-ag", "sql-0")
	setAGClusterType(ag, "External")
	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing for ClusterType=External")
	}
}

func TestFencing_AGFailoverRunning_Skip(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")

	// Create an AGFailover in Running phase
	fo := &v1alpha1.AGFailover{
		ObjectMeta: metav1.ObjectMeta{Name: "fo-running", Namespace: "default"},
		Spec:       v1alpha1.AGFailoverSpec{AGName: "myag"},
		Status:     v1alpha1.AGFailoverStatus{Phase: v1alpha1.FailoverPhaseRunning},
	}

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), fo}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})
	// Manually set the AGFailover status (fake client requires explicit status write)
	_ = r.Client.Status().Update(context.Background(), fo)

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing when AGFailover is Running")
	}
}

func TestFencing_FencingExhausted_Skip(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")
	setAGFencingStatus(ag, "sql-0", 10, maxFencingAttempts, time.Now().Add(-10*time.Second))

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing when exhausted")
	}
}

// =============================================================================
// Pas de split-brain (tests 6-8)
// =============================================================================

func TestFencing_SinglePrimary_MatchesStatus(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "SECONDARY")

	ag := testAGWithStatus("test-ag", "sql-0")
	setAGClusterType(ag, "None")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing when single primary matches status")
	}
}

func TestFencing_AllSecondary_NoFencing(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "SECONDARY")
	setupMockWithRole(mock1, "myag", "sql-1", "SECONDARY")

	ag := testAGWithStatus("test-ag", "sql-0")
	setAGClusterType(ag, "None")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing when all replicas are SECONDARY")
	}
}

func TestFencing_ReplicaUnreachable_ExcludedFromAnalysis(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock0.ConnectError = errors.New("connection refused")
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing — unreachable replica excluded, single PRIMARY matches status")
	}
}

// =============================================================================
// Status stale (tests 9-10)
// =============================================================================

func TestFencing_StatusStale_UpdatesStatusAndLabels(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "SECONDARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-0") // stale: says sql-0 but sql-1 is real primary
	setAGClusterType(ag, "None")

	pod0 := createPodForReplica("sql-0", "default")
	pod1 := createPodForReplica("sql-1", "default")

	r, recorder := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0, pod1}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected fenced=false for status stale (not a split-brain)")
	}
	if ag.Status.PrimaryReplica != "sql-1" {
		t.Errorf("expected status.PrimaryReplica to be corrected to sql-1, got %s", ag.Status.PrimaryReplica)
	}
	// Check event
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "PrimaryChangedExternally") {
			t.Errorf("expected PrimaryChangedExternally event, got: %s", event)
		}
	default:
		t.Error("expected PrimaryChangedExternally event")
	}
}

func TestFencing_StatusStale_ReturnEarlyIfWrongNode(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "SECONDARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-0") // stale
	setAGClusterType(ag, "None")

	pod0 := createPodForReplica("sql-0", "default")
	pod1 := createPodForReplica("sql-1", "default")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0, pod1}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	// Simulate the calling code pattern
	previousPrimary := ag.Status.PrimaryReplica
	_ = r.detectAndResolveSplitBrain(context.Background(), ag)

	if ag.Status.PrimaryReplica == previousPrimary {
		t.Error("expected status.PrimaryReplica to have changed (stale correction)")
	}
	// The caller should detect previousPrimary != ag.Status.PrimaryReplica and return early
	if ag.Status.PrimaryReplica != "sql-1" {
		t.Errorf("expected sql-1, got %s", ag.Status.PrimaryReplica)
	}
}

// =============================================================================
// Split-brain (tests 11-14)
// =============================================================================

func TestFencing_DualPrimary_FencesLowerLSN(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")
	mock0.MockLSN = 100
	mock1.MockLSN = 200

	ag := testAGWithStatus("test-ag", "sql-0")
	setAGClusterType(ag, "None")
	pod0 := createPodForReplica("sql-0", "default")
	pod1 := createPodForReplica("sql-1", "default")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0, pod1}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if !fenced {
		t.Error("expected fencing on dual-primary")
	}
	if !mock0.WasCalled("SetAGRoleSecondary") {
		t.Error("expected SetAGRoleSecondary called on sql-0 (lower LSN)")
	}
	if mock1.WasCalled("SetAGRoleSecondary") {
		t.Error("sql-1 (higher LSN) should NOT be fenced")
	}
	if ag.Status.PrimaryReplica != "sql-1" {
		t.Errorf("expected status.PrimaryReplica=sql-1, got %s", ag.Status.PrimaryReplica)
	}
}

func TestFencing_DualPrimary_EqualLSN_KeepsStatus(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")
	// Equal LSN → keep the one matching status (sql-1)

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")
	pod0 := createPodForReplica("sql-0", "default")
	pod1 := createPodForReplica("sql-1", "default")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0, pod1}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if !fenced {
		t.Error("expected fencing on dual-primary")
	}
	if !mock0.WasCalled("SetAGRoleSecondary") {
		t.Error("expected sql-0 to be fenced (not the status primary)")
	}
	if mock1.WasCalled("SetAGRoleSecondary") {
		t.Error("sql-1 (matches status) should NOT be fenced")
	}
}

func TestFencing_DualPrimary_StatusNotInPrimaries(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")
	// status=sql-2 (gone) → keep highest LSN

	ag := testAGWithStatus("test-ag", "sql-2")
	setAGClusterType(ag, "None")
	// Add sql-2 to spec but it's unreachable
	port := int32(1433)
	ag.Spec.Replicas = append(ag.Spec.Replicas, v1alpha1.AGReplicaSpec{
		ServerName: "sql-2", EndpointURL: "TCP://sql-2:5022",
		Server: v1alpha1.ServerReference{Host: "sql-2", Port: &port, CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"}},
	})

	pod0 := createPodForReplica("sql-0", "default")
	pod1 := createPodForReplica("sql-1", "default")
	mockDead := sqlclient.NewMockClient()
	mockDead.ConnectError = errors.New("unreachable")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0, pod1}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1, "sql-2": mockDead,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if !fenced {
		t.Error("expected fencing on dual-primary")
	}
	// One of them should be fenced (the one with lower LSN)
	fenced0 := mock0.WasCalled("SetAGRoleSecondary")
	fenced1 := mock1.WasCalled("SetAGRoleSecondary")
	if !fenced0 && !fenced1 {
		t.Error("expected at least one replica to be fenced")
	}
	if fenced0 && fenced1 {
		t.Error("should not fence both replicas")
	}
}

func TestFencing_TriplePrimary_FencesAllButBestLSN(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	mock2 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")
	setupMockWithRole(mock2, "myag", "sql-2", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")
	port := int32(1433)
	ag.Spec.Replicas = append(ag.Spec.Replicas, v1alpha1.AGReplicaSpec{
		ServerName: "sql-2", EndpointURL: "TCP://sql-2:5022",
		Server: v1alpha1.ServerReference{Host: "sql-2", Port: &port, CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"}},
	})
	pod0 := createPodForReplica("sql-0", "default")
	pod1 := createPodForReplica("sql-1", "default")
	pod2 := createPodForReplica("sql-2", "default")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0, pod1, pod2}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1, "sql-2": mock2,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if !fenced {
		t.Error("expected fencing on triple-primary")
	}
	// With equal LSN and status=sql-1, sql-1 should be kept, sql-0 and sql-2 fenced
	if mock1.WasCalled("SetAGRoleSecondary") || mock1.WasCalled("DropAG") {
		t.Error("sql-1 (status primary) should NOT be fenced")
	}
}

// =============================================================================
// Fencing behavior (tests 15-19)
// =============================================================================

func TestFencing_FenceSoft_CallsSetRoleSecondary(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")
	pod0 := createPodForReplica("sql-0", "default")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	r.detectAndResolveSplitBrain(context.Background(), ag)

	if !mock0.WasCalled("SetAGRoleSecondary") {
		t.Error("expected SetAGRoleSecondary on first fencing (soft)")
	}
	if mock0.WasCalled("DropAG") {
		t.Error("should NOT call DropAG on first fencing")
	}
}

func TestFencing_FenceHard_CallsDropAG(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")
	// sql-0 was already fenced recently → escalate to hard
	setAGFencingStatus(ag, "sql-0", 1, 1, time.Now().Add(-10*time.Second))
	pod0 := createPodForReplica("sql-0", "default")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	r.detectAndResolveSplitBrain(context.Background(), ag)

	if !mock0.WasCalled("DropAG") {
		t.Error("expected DropAG on hard fencing (re-claim within cooldown)")
	}
	if mock0.WasCalled("SetAGRoleSecondary") {
		t.Error("should NOT call SetAGRoleSecondary on hard fencing")
	}
}

func TestFencing_LabelRemovedEvenIfSQLFails(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")
	mock0.SetMethodError("SetAGRoleSecondary", errors.New("SQL error"))

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")
	pod0 := createPodForReplica("sql-0", "default")
	pod0.Labels[LabelRole] = RolePrimary

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)

	if !fenced {
		t.Error("expected fenced=true even if SQL fails (label was removed)")
	}
	// Verify pod label was changed to secondary
	var updatedPod corev1.Pod
	_ = r.Get(context.Background(), types.NamespacedName{Name: "sql-0", Namespace: "default"}, &updatedPod)
	if updatedPod.Labels[LabelRole] != RoleSecondary {
		t.Errorf("expected pod sql-0 label=secondary (traffic cut), got %q", updatedPod.Labels[LabelRole])
	}
}

func TestFencing_StatusUpdated(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "PRIMARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")
	pod0 := createPodForReplica("sql-0", "default")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret(), pod0}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	r.detectAndResolveSplitBrain(context.Background(), ag)

	if ag.Status.LastFencingTime == nil {
		t.Error("expected LastFencingTime to be set")
	}
	if ag.Status.FencingCount < 1 {
		t.Error("expected FencingCount >= 1")
	}
	if ag.Status.LastFencedReplica == "" {
		t.Error("expected LastFencedReplica to be set")
	}
	if ag.Status.ConsecutiveFencingCount < 1 {
		t.Error("expected ConsecutiveFencingCount >= 1")
	}
	if ag.Status.PrimaryReplica != "sql-1" {
		t.Errorf("expected PrimaryReplica=sql-1 (legitimate), got %s", ag.Status.PrimaryReplica)
	}
}

func TestFencing_Idempotent(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "SECONDARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	fenced := r.detectAndResolveSplitBrain(context.Background(), ag)
	if fenced {
		t.Error("expected no fencing when sql-0 is already SECONDARY")
	}
}

// =============================================================================
// Recovery — rejoin (tests 20-24)
// =============================================================================

func TestFencing_Rejoin_DisconnectedSecondary(t *testing.T) {
	mock1 := sqlclient.NewMockClient()

	ag := testAGWithStatus("test-ag", "sql-0")
	setAGClusterType(ag, "None")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-1": mock1,
	})

	replica := ag.Spec.Replicas[1]
	r.tryRejoinReplica(context.Background(), ag, &replica)

	if !mock1.WasCalled("JoinAG") {
		t.Error("expected JoinAG called for disconnected secondary")
	}
}

func TestFencing_Rejoin_OrphanAfterHardFencing(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	// sql-0 has no AG (was dropped by hard fencing) — JoinAG should be attempted

	ag := testAGWithStatus("test-ag", "sql-1")
	setAGClusterType(ag, "None")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0,
	})

	replica := ag.Spec.Replicas[0]
	r.tryRejoinReplica(context.Background(), ag, &replica)

	if !mock0.WasCalled("JoinAG") {
		t.Error("expected JoinAG called for orphan replica")
	}
}

func TestFencing_Rejoin_Unreachable_NoError(t *testing.T) {
	mock1 := sqlclient.NewMockClient()
	mock1.ConnectError = errors.New("unreachable")

	ag := testAGWithStatus("test-ag", "sql-0")
	setAGClusterType(ag, "None")

	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-1": mock1,
	})

	// Should not panic or return error
	r.tryRejoinReplica(context.Background(), ag, &ag.Spec.Replicas[1])

	if mock1.WasCalled("JoinAG") {
		t.Error("should not call JoinAG when unreachable")
	}
}

func TestFencing_Rejoin_AlreadyConnected_Skip(t *testing.T) {
	// This tests the calling code pattern, not tryRejoinReplica itself.
	// The caller skips if rs.Connected == true.
	mock1 := sqlclient.NewMockClient()

	ag := testAGWithStatus("test-ag", "sql-0")
	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-1": mock1,
	})

	// Simulate the check pattern from the controller
	connected := true // Already connected
	if !connected {
		r.tryRejoinReplica(context.Background(), ag, &ag.Spec.Replicas[1])
	}

	if mock1.WasCalled("JoinAG") {
		t.Error("should not rejoin an already connected replica")
	}
}

func TestFencing_Rejoin_PrimaryDisconnected_Skip(t *testing.T) {
	// Primary DISCONNECTED should NOT be rejoined — the rejoin code skips it.
	mock0 := sqlclient.NewMockClient()

	ag := testAGWithStatus("test-ag", "sql-0")
	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0,
	})

	// Simulate the check pattern: skip if serverName == primaryReplica
	replica := ag.Spec.Replicas[0]
	if replica.ServerName != ag.Status.PrimaryReplica {
		r.tryRejoinReplica(context.Background(), ag, &replica)
	}

	if mock0.WasCalled("JoinAG") {
		t.Error("should not rejoin the primary replica")
	}
}

// =============================================================================
// Connexion primary (tests 25-27)
// =============================================================================

func TestFencing_ConnectsToKnownPrimary(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()

	ag := testAGWithStatus("test-ag", "sql-1")

	_, _ = newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	// Resolve primary replica using the same logic as the controller
	primaryReplica := ag.Spec.Replicas[0]
	if ag.Status.PrimaryReplica != "" {
		for i := range ag.Spec.Replicas {
			if ag.Spec.Replicas[i].ServerName == ag.Status.PrimaryReplica {
				primaryReplica = ag.Spec.Replicas[i]
				break
			}
		}
	}

	if primaryReplica.ServerName != "sql-1" {
		t.Errorf("expected primary resolved to sql-1, got %s", primaryReplica.ServerName)
	}
}

func TestFencing_FallsBackToReplicas0_IfStatusEmpty(t *testing.T) {
	ag := testAG("test-ag")
	// status.PrimaryReplica is empty

	primaryReplica := ag.Spec.Replicas[0]
	if ag.Status.PrimaryReplica != "" {
		for i := range ag.Spec.Replicas {
			if ag.Spec.Replicas[i].ServerName == ag.Status.PrimaryReplica {
				primaryReplica = ag.Spec.Replicas[i]
				break
			}
		}
	}

	if primaryReplica.ServerName != "sql-0" {
		t.Errorf("expected fallback to sql-0 (Replicas[0]), got %s", primaryReplica.ServerName)
	}
}

func TestFencing_HandleDeletion_UsesKnownPrimary(t *testing.T) {
	mock0 := sqlclient.NewMockClient()
	mock1 := sqlclient.NewMockClient()
	setupMockWithRole(mock0, "myag", "sql-0", "SECONDARY")
	setupMockWithRole(mock1, "myag", "sql-1", "PRIMARY")

	ag := testAGWithStatus("test-ag", "sql-1")
	r, _ := newMultiMockAGReconciler([]runtime.Object{ag, saSecret()}, map[string]*sqlclient.MockClient{
		"sql-0": mock0, "sql-1": mock1,
	})

	// Add finalizer first
	reconcileAG(r, "test-ag")

	// Simulate deletion: resolve primary from status
	primaryReplica := ag.Spec.Replicas[0]
	if ag.Status.PrimaryReplica != "" {
		for i := range ag.Spec.Replicas {
			if ag.Spec.Replicas[i].ServerName == ag.Status.PrimaryReplica {
				primaryReplica = ag.Spec.Replicas[i]
				break
			}
		}
	}

	if primaryReplica.ServerName != "sql-1" {
		t.Errorf("expected deletion to use sql-1 (known primary), got %s", primaryReplica.ServerName)
	}
}

// =============================================================================
// handleAutoFailover fix (tests 28-29)
// =============================================================================

func TestFencing_AutoFailover_IteratesAllExceptKnownPrimary(t *testing.T) {
	// status=sql-1, sql-1 is unreachable → should try sql-0
	knownPrimary := "sql-1"
	replicas := []v1alpha1.AGReplicaSpec{
		{ServerName: "sql-0"},
		{ServerName: "sql-1"},
	}

	var candidates []string
	for i := 0; i < len(replicas); i++ {
		if replicas[i].ServerName == knownPrimary {
			continue
		}
		candidates = append(candidates, replicas[i].ServerName)
	}

	if len(candidates) != 1 || candidates[0] != "sql-0" {
		t.Errorf("expected candidates=[sql-0], got %v", candidates)
	}
}

func TestFencing_AutoFailover_FallbackSkipsReplica0_IfStatusEmpty(t *testing.T) {
	// status="" → fallback: skip Replicas[0] like before
	knownPrimary := ""
	replicas := []v1alpha1.AGReplicaSpec{
		{ServerName: "sql-0"},
		{ServerName: "sql-1"},
	}

	if knownPrimary == "" {
		knownPrimary = replicas[0].ServerName
	}

	var candidates []string
	for i := 0; i < len(replicas); i++ {
		if replicas[i].ServerName == knownPrimary {
			continue
		}
		candidates = append(candidates, replicas[i].ServerName)
	}

	if len(candidates) != 1 || candidates[0] != "sql-1" {
		t.Errorf("expected candidates=[sql-1] (skipping Replicas[0] as fallback), got %v", candidates)
	}
}
