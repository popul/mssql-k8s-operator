package controller

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

var errTest = errors.New("test error")

func newTestPermissionReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*PermissionReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Permission{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &PermissionReconciler{
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

func testPermission(name string, grants []v1alpha1.PermissionEntry, denies []v1alpha1.PermissionEntry) *v1alpha1.Permission {
	port := int32(1433)
	return &v1alpha1.Permission{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.PermissionSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			DatabaseName: "mydb",
			UserName:     "appuser",
			Grants:       grants,
			Denies:       denies,
		},
	}
}

// --- Test: CR not found → no error ---
func TestPermissionReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestPermissionReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Error("expected no requeue for not-found CR")
	}
}

// --- Test: Happy path grant → Ready=True ---
func TestPermissionReconcile_GrantPermission(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	// First reconcile adds finalizer
	r.Reconcile(context.Background(), reqFor("myperm"))
	// Second reconcile applies permissions
	result, err := r.Reconcile(context.Background(), reqFor("myperm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("GrantPermission") {
		t.Error("expected GrantPermission to be called")
	}

	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter for ready state")
	}

	var updated v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &updated)
	readyFound := false
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionTrue {
			readyFound = true
		}
	}
	if !readyFound {
		t.Error("expected Ready=True condition")
	}
}

// --- Test: Deny permission ---
func TestPermissionReconcile_DenyPermission(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	denies := []v1alpha1.PermissionEntry{
		{Permission: "DELETE", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", nil, denies)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))
	_, err := r.Reconcile(context.Background(), reqFor("myperm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("DenyPermission") {
		t.Error("expected DenyPermission to be called")
	}
}

// --- Test: Revoke removed permissions ---
func TestPermissionReconcile_RevokeRemoved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()

	// Pre-grant SELECT on SCHEMA::app
	mockSQL.GrantPermission(context.Background(), "mydb", "SELECT", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	// CR only has INSERT, not SELECT → SELECT should be revoked
	grants := []v1alpha1.PermissionEntry{
		{Permission: "INSERT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))
	_, err := r.Reconcile(context.Background(), reqFor("myperm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("RevokePermission") {
		t.Error("expected RevokePermission to be called for removed SELECT")
	}
	if !mockSQL.WasCalled("GrantPermission") {
		t.Error("expected GrantPermission to be called for new INSERT")
	}
}

// --- Test: Already granted → no duplicate grant ---
func TestPermissionReconcile_AlreadyGranted(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()

	// Pre-grant the same permission
	mockSQL.GrantPermission(context.Background(), "mydb", "SELECT", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))
	_, err := r.Reconcile(context.Background(), reqFor("myperm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("GrantPermission") {
		t.Error("should NOT call GrantPermission when already granted")
	}
}

// --- Test: Secret not found → Ready=False ---
func TestPermissionReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))
	_, err := r.Reconcile(context.Background(), reqFor("myperm"))
	if err != nil {
		t.Fatalf("permanent error should not be returned: %v", err)
	}

	var updated v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &updated)
	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ReasonSecretNotFound {
			found = true
		}
	}
	if !found {
		t.Error("expected Ready=False with ReasonSecretNotFound")
	}
}

// --- Test: Connection error → transient error ---
func TestPermissionReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))
	_, err := r.Reconcile(context.Background(), reqFor("myperm"))
	if err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// --- Test: Deletion revokes all ---
func TestPermissionReconcile_DeletionRevokesAll(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
		{Permission: "INSERT", On: "SCHEMA::app"},
	}
	denies := []v1alpha1.PermissionEntry{
		{Permission: "DELETE", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, denies)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	// Reconcile to add finalizer
	r.Reconcile(context.Background(), reqFor("myperm"))

	var current v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &current)
	current.Finalizers = []string{v1alpha1.Finalizer}
	mockSQL.ResetCalls()

	_, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should revoke 3 permissions (2 grants + 1 deny)
	if mockSQL.CallCount("RevokePermission") != 3 {
		t.Errorf("expected 3 RevokePermission calls, got %d", mockSQL.CallCount("RevokePermission"))
	}
}

// --- Test: Idempotence ---
func TestPermissionReconcile_Idempotent(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	// Reconcile multiple times
	for i := 0; i < 3; i++ {
		_, err := r.Reconcile(context.Background(), reqFor("myperm"))
		if err != nil {
			t.Fatalf("reconcile %d: unexpected error: %v", i, err)
		}
	}

	// GrantPermission should be called only once
	if mockSQL.CallCount("GrantPermission") != 1 {
		t.Errorf("expected GrantPermission called once, got %d", mockSQL.CallCount("GrantPermission"))
	}
}

// --- Test: Grant error → transient error ---
func TestPermissionReconcile_GrantError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("GrantPermission", errTest)

	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))
	_, err := r.Reconcile(context.Background(), reqFor("myperm"))
	if err == nil {
		t.Error("expected error when GrantPermission fails")
	}
}
