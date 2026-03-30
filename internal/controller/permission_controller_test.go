package controller

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

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

func getPermCondition(perm *v1alpha1.Permission, condType string) *metav1.Condition {
	for i := range perm.Status.Conditions {
		if perm.Status.Conditions[i].Type == condType {
			return &perm.Status.Conditions[i]
		}
	}
	return nil
}

// reconcilePerm runs Reconcile twice: first to add finalizer, second to actually reconcile.
func reconcilePerm(r *PermissionReconciler, name string) error {
	r.Reconcile(context.Background(), reqFor(name))
	_, err := r.Reconcile(context.Background(), reqFor(name))
	return err
}

// =============================================================================
// Critère 1: Grant — permissions listées dans grants → GRANT
// =============================================================================

func TestPermissionReconcile_Grant(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("GrantPermission") {
		t.Error("expected GrantPermission to be called")
	}

	var updated v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &updated)
	cond := getPermCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True")
	}
}

// =============================================================================
// Critère 2: Deny — permissions listées dans denies → DENY
// =============================================================================

func TestPermissionReconcile_Deny(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	denies := []v1alpha1.PermissionEntry{
		{Permission: "DELETE", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", nil, denies)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("DenyPermission") {
		t.Error("expected DenyPermission to be called")
	}
}

// =============================================================================
// Critère 3: Idempotence grant — déjà GRANT → pas de re-GRANT
// =============================================================================

func TestPermissionReconcile_IdempotentGrant(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.GrantPermission(context.Background(), "mydb", "SELECT", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("GrantPermission") {
		t.Error("should NOT re-GRANT when already granted")
	}
}

// =============================================================================
// Critère 4: Idempotence deny — déjà DENY → pas de re-DENY
// =============================================================================

func TestPermissionReconcile_IdempotentDeny(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.DenyPermission(context.Background(), "mydb", "DELETE", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	denies := []v1alpha1.PermissionEntry{
		{Permission: "DELETE", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", nil, denies)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("DenyPermission") {
		t.Error("should NOT re-DENY when already denied")
	}
}

// =============================================================================
// Critère 5: Revoke removed grant
// =============================================================================

func TestPermissionReconcile_RevokeRemovedGrant(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-grant SELECT that is NOT in the spec
	mockSQL.GrantPermission(context.Background(), "mydb", "SELECT", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	// Spec only has INSERT
	grants := []v1alpha1.PermissionEntry{
		{Permission: "INSERT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("RevokePermission") {
		t.Error("expected RevokePermission for removed SELECT grant")
	}
	if !mockSQL.WasCalled("GrantPermission") {
		t.Error("expected GrantPermission for new INSERT grant")
	}
}

// =============================================================================
// Critère 6: Revoke removed deny
// =============================================================================

func TestPermissionReconcile_RevokeRemovedDeny(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-deny DELETE that is NOT in the spec
	mockSQL.DenyPermission(context.Background(), "mydb", "DELETE", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	// Spec has no denies
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("RevokePermission") {
		t.Error("expected RevokePermission for removed DENY")
	}
}

// =============================================================================
// Critère 7: Grant → Deny transition
// =============================================================================

func TestPermissionReconcile_GrantToDenyTransition(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Currently GRANT SELECT
	mockSQL.GrantPermission(context.Background(), "mydb", "SELECT", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	// Spec moves SELECT to denies
	denies := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", nil, denies)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("DenyPermission") {
		t.Error("expected DenyPermission for transition from GRANT to DENY")
	}
	if !mockSQL.WasCalled("RevokePermission") {
		t.Error("expected RevokePermission to remove old GRANT")
	}
}

// =============================================================================
// Critère 8: Deny → Grant transition
// =============================================================================

func TestPermissionReconcile_DenyToGrantTransition(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Currently DENY SELECT
	mockSQL.DenyPermission(context.Background(), "mydb", "SELECT", "SCHEMA::app", "appuser")
	mockSQL.ResetCalls()

	// Spec moves SELECT to grants
	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("GrantPermission") {
		t.Error("expected GrantPermission for transition from DENY to GRANT")
	}
	if !mockSQL.WasCalled("RevokePermission") {
		t.Error("expected RevokePermission to remove old DENY")
	}
}

// =============================================================================
// Critère 9: Mixed grants + denies
// =============================================================================

func TestPermissionReconcile_MixedGrantsAndDenies(t *testing.T) {
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

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.CallCount("GrantPermission") != 2 {
		t.Errorf("expected 2 GrantPermission calls, got %d", mockSQL.CallCount("GrantPermission"))
	}
	if mockSQL.CallCount("DenyPermission") != 1 {
		t.Errorf("expected 1 DenyPermission call, got %d", mockSQL.CallCount("DenyPermission"))
	}
}

// =============================================================================
// Critère 10: ObservedGeneration
// =============================================================================

func TestPermissionReconcile_ObservedGeneration(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	reconcilePerm(r, "myperm")

	var updated v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &updated)
	if updated.Status.ObservedGeneration != 1 {
		t.Errorf("expected ObservedGeneration=1, got %d", updated.Status.ObservedGeneration)
	}
}

// =============================================================================
// Critère 11: RequeueAfter avec jitter
// =============================================================================

func TestPermissionReconcile_RequeueWithJitter(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))

	seen := make(map[int64]bool)
	for i := 0; i < 10; i++ {
		result, _ := r.Reconcile(context.Background(), reqFor("myperm"))
		seen[result.RequeueAfter.Milliseconds()] = true
	}
	if len(seen) < 2 {
		t.Error("expected jitter to produce varying RequeueAfter values")
	}
}

// =============================================================================
// Critère 12: Secret non trouvé → Ready=False
// =============================================================================

func TestPermissionReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm}, mockSQL) // no Secret

	if err := reconcilePerm(r, "myperm"); err != nil {
		t.Fatalf("permanent error should not be returned: %v", err)
	}

	var updated v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &updated)
	cond := getPermCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != v1alpha1.ReasonSecretNotFound {
		t.Errorf("expected Ready=False/SecretNotFound, got %+v", cond)
	}
}

// =============================================================================
// Critère 13: Connexion SQL échoue → erreur transitoire
// =============================================================================

func TestPermissionReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	err := reconcilePerm(r, "myperm")
	if err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// =============================================================================
// Critère 14: GrantPermission échoue → erreur transitoire
// =============================================================================

func TestPermissionReconcile_GrantError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("GrantPermission", fmt.Errorf("grant failed"))
	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	err := reconcilePerm(r, "myperm")
	if err == nil {
		t.Error("expected error when GrantPermission fails")
	}
}

// =============================================================================
// Critère 15: DenyPermission échoue → erreur transitoire
// =============================================================================

func TestPermissionReconcile_DenyError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("DenyPermission", fmt.Errorf("deny failed"))
	denies := []v1alpha1.PermissionEntry{
		{Permission: "DELETE", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", nil, denies)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	err := reconcilePerm(r, "myperm")
	if err == nil {
		t.Error("expected error when DenyPermission fails")
	}
}

// =============================================================================
// Critère 16: RevokePermission échoue → erreur transitoire
// =============================================================================

func TestPermissionReconcile_RevokeError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-grant something not in spec
	mockSQL.GrantPermission(context.Background(), "mydb", "SELECT", "SCHEMA::app", "appuser")
	mockSQL.SetMethodError("RevokePermission", fmt.Errorf("revoke failed"))
	mockSQL.ResetCalls()

	// Spec has no grants → should try to revoke
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	err := reconcilePerm(r, "myperm")
	if err == nil {
		t.Error("expected error when RevokePermission fails")
	}
}

// =============================================================================
// Critère 17: GetPermissions échoue → erreur transitoire
// =============================================================================

func TestPermissionReconcile_GetPermissionsError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = nil
	// We need to inject error at GetPermissions level
	// Since MockClient.GetPermissions checks checkConnect only, let's use MethodErrors
	// Actually GetPermissions doesn't check MethodErrors, but uses checkConnect.
	// We'll set ConnectError after factory returns the client
	// Workaround: use a custom factory
	perm := testPermission("myperm", nil, nil)

	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Permission{}).
		WithRuntimeObjects(perm, saSecret()).Build()
	recorder := record.NewFakeRecorder(20)

	callCount := 0
	r := &PermissionReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: recorder,
		SQLClientFactory: func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
			callCount++
			m := sqlclient.NewMockClient()
			if callCount > 1 { // second reconcile (after finalizer)
				m.ConnectError = fmt.Errorf("get permissions failed")
			}
			return m, nil
		},
	}

	err := reconcilePerm(r, "myperm")
	if err == nil {
		t.Error("expected error when GetPermissions fails")
	}
}

// =============================================================================
// Critère 18: Deletion → REVOKE all grants and denies
// =============================================================================

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

	r.Reconcile(context.Background(), reqFor("myperm"))

	var current v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &current)
	current.Finalizers = []string{v1alpha1.Finalizer}
	mockSQL.ResetCalls()

	_, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.CallCount("RevokePermission") != 3 {
		t.Errorf("expected 3 RevokePermission calls (2 grants + 1 deny), got %d", mockSQL.CallCount("RevokePermission"))
	}
}

// =============================================================================
// Critère 19: Deletion + connexion perdue → log + finalizer retiré
// =============================================================================

func TestPermissionReconcile_DeletionConnectionLost(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest

	grants := []v1alpha1.PermissionEntry{
		{Permission: "SELECT", On: "SCHEMA::app"},
	}
	perm := testPermission("myperm", grants, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myperm"))

	var current v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &current)
	current.Finalizers = []string{v1alpha1.Finalizer}

	_, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("deletion should not block on connection error: %v", err)
	}
}

// =============================================================================
// Critère 20: Pas de finalizer → retour immédiat
// =============================================================================

func TestPermissionReconcile_DeletionNoFinalizer(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	perm := testPermission("myperm", nil, nil)
	r, _ := newTestPermissionReconciler([]runtime.Object{perm, saSecret()}, mockSQL)

	var current v1alpha1.Permission
	r.Client.Get(context.Background(), reqFor("myperm").NamespacedName, &current)

	result, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Error("expected immediate return")
	}
}

// =============================================================================
// Critère 20 bis: CR not found → no error
// =============================================================================

func TestPermissionReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestPermissionReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Error("expected no requeue")
	}
}
