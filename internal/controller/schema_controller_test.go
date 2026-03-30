package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

func newTestSchemaReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*SchemaReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Schema{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &SchemaReconciler{
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

func testSchema(name string, owner *string, deletionPolicy *v1alpha1.DeletionPolicy) *v1alpha1.Schema {
	port := int32(1433)
	return &v1alpha1.Schema{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.SchemaSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			DatabaseName:   "mydb",
			SchemaName:     "app",
			Owner:          owner,
			DeletionPolicy: deletionPolicy,
		},
	}
}

func strPtr(s string) *string { return &s }

func getSchemaCondition(schema *v1alpha1.Schema, condType string) *metav1.Condition {
	for i := range schema.Status.Conditions {
		if schema.Status.Conditions[i].Type == condType {
			return &schema.Status.Conditions[i]
		}
	}
	return nil
}

// reconcileSchema runs Reconcile twice: first to add finalizer, second to actually reconcile.
func reconcileSchema(r *SchemaReconciler, name string) error {
	r.Reconcile(context.Background(), reqFor(name))
	_, err := r.Reconcile(context.Background(), reqFor(name))
	return err
}

// =============================================================================
// Critère 1: Création — schema n'existe pas → CREATE SCHEMA + Ready=True
// =============================================================================

func TestSchemaReconcile_Creation(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	r, recorder := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	if err := reconcileSchema(r, "myschema"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("CreateSchema") {
		t.Error("expected CreateSchema to be called")
	}

	var updated v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &updated)
	cond := getSchemaCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True condition")
	}

	// Check event emitted
	select {
	case evt := <-recorder.Events:
		if evt == "" {
			t.Error("expected SchemaCreated event")
		}
	default:
		// events from first reconcile may have been consumed
	}
}

// =============================================================================
// Critère 2: Idempotence — schema existe déjà → pas de CREATE
// =============================================================================

func TestSchemaReconcile_AlreadyExists(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	if err := reconcileSchema(r, "myschema"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("CreateSchema") {
		t.Error("should NOT call CreateSchema when schema already exists")
	}

	var updated v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &updated)
	cond := getSchemaCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Ready=True")
	}
}

// =============================================================================
// Critère 3: Owner initial — CREATE avec AUTHORIZATION
// =============================================================================

func TestSchemaReconcile_CreationWithOwner(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", strPtr("appuser"), nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	if err := reconcileSchema(r, "myschema"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("CreateSchema") {
		t.Error("expected CreateSchema to be called")
	}
}

// =============================================================================
// Critère 4: Owner drift — owner différent → SetSchemaOwner
// =============================================================================

func TestSchemaReconcile_OwnerDrift(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-create with default owner "dbo"
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	schema := testSchema("myschema", strPtr("appuser"), nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	if err := reconcileSchema(r, "myschema"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("SetSchemaOwner") {
		t.Error("expected SetSchemaOwner to be called for owner drift")
	}
}

// =============================================================================
// Critère 5: Owner nil → pas d'appel SetSchemaOwner
// =============================================================================

func TestSchemaReconcile_NoOwner_NoSetOwner(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	if err := reconcileSchema(r, "myschema"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("SetSchemaOwner") {
		t.Error("should NOT call SetSchemaOwner when owner is nil")
	}
	if mockSQL.WasCalled("GetSchemaOwner") {
		t.Error("should NOT call GetSchemaOwner when owner is nil")
	}
}

// =============================================================================
// Critère 6: ObservedGeneration mis à jour
// =============================================================================

func TestSchemaReconcile_ObservedGeneration(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	reconcileSchema(r, "myschema")

	var updated v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &updated)
	if updated.Status.ObservedGeneration != 1 {
		t.Errorf("expected ObservedGeneration=1, got %d", updated.Status.ObservedGeneration)
	}
}

// =============================================================================
// Critère 7: RequeueAfter avec jitter
// =============================================================================

func TestSchemaReconcile_RequeueWithJitter(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// Add finalizer first
	r.Reconcile(context.Background(), reqFor("myschema"))

	seen := make(map[time.Duration]bool)
	for i := 0; i < 10; i++ {
		result, _ := r.Reconcile(context.Background(), reqFor("myschema"))
		seen[result.RequeueAfter] = true
	}
	if len(seen) < 2 {
		t.Error("expected jitter to produce varying RequeueAfter values")
	}
}

// =============================================================================
// Critère 8: Secret non trouvé → Ready=False, SecretNotFound
// =============================================================================

func TestSchemaReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema}, mockSQL) // no Secret

	if err := reconcileSchema(r, "myschema"); err != nil {
		t.Fatalf("permanent error should not be returned: %v", err)
	}

	var updated v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &updated)
	cond := getSchemaCondition(&updated, v1alpha1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != v1alpha1.ReasonSecretNotFound {
		t.Errorf("expected Ready=False/SecretNotFound, got %+v", cond)
	}
}

// =============================================================================
// Critère 10: Connexion SQL échoue → erreur transitoire
// =============================================================================

func TestSchemaReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	err := reconcileSchema(r, "myschema")
	if err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// =============================================================================
// Critère 11: CreateSchema échoue → erreur transitoire
// =============================================================================

func TestSchemaReconcile_CreateSchemaError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("CreateSchema", fmt.Errorf("deadlock"))
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	err := reconcileSchema(r, "myschema")
	if err == nil {
		t.Error("expected transient error when CreateSchema fails")
	}
}

// =============================================================================
// Critère 12: GetSchemaOwner échoue → erreur transitoire
// =============================================================================

func TestSchemaReconcile_GetSchemaOwnerError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.SetMethodError("GetSchemaOwner", fmt.Errorf("timeout"))
	mockSQL.ResetCalls()

	schema := testSchema("myschema", strPtr("appuser"), nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	err := reconcileSchema(r, "myschema")
	if err == nil {
		t.Error("expected transient error when GetSchemaOwner fails")
	}
}

// =============================================================================
// Critère 13: SetSchemaOwner échoue → erreur transitoire
// =============================================================================

func TestSchemaReconcile_SetSchemaOwnerError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.SetMethodError("SetSchemaOwner", fmt.Errorf("permission denied"))
	mockSQL.ResetCalls()

	schema := testSchema("myschema", strPtr("appuser"), nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	err := reconcileSchema(r, "myschema")
	if err == nil {
		t.Error("expected transient error when SetSchemaOwner fails")
	}
}

// =============================================================================
// Critère 14: Deletion + Retain → pas de DROP, finalizer retiré
// =============================================================================

func TestSchemaReconcile_DeletionRetain(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	schema := testSchema("myschema", nil, nil) // default is Retain
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// Reconcile to add finalizer
	r.Reconcile(context.Background(), reqFor("myschema"))

	var current v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &current)
	result, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("expected no requeue after retain deletion")
	}
	if mockSQL.WasCalled("DropSchema") {
		t.Error("should NOT drop schema with Retain policy")
	}
}

// =============================================================================
// Critère 15: Deletion + Delete → DROP SCHEMA + finalizer retiré
// =============================================================================

func TestSchemaReconcile_DeletionDelete(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	policy := v1alpha1.DeletionPolicyDelete
	schema := testSchema("myschema", nil, &policy)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myschema"))

	var current v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &current)
	current.Finalizers = []string{v1alpha1.Finalizer}

	result, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("expected no requeue after successful deletion")
	}
	if !mockSQL.WasCalled("DropSchema") {
		t.Error("expected DropSchema to be called")
	}
}

// =============================================================================
// Critère 16: Deletion bloquée — schema contient des objets → RequeueAfter
// =============================================================================

func TestSchemaReconcile_DeletionBlockedByObjects(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.SetSchemaHasObjects("mydb", "app", true)
	mockSQL.ResetCalls()

	policy := v1alpha1.DeletionPolicyDelete
	schema := testSchema("myschema", nil, &policy)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myschema"))

	var current v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &current)
	current.Finalizers = []string{v1alpha1.Finalizer}

	result, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter when schema has objects")
	}
	if mockSQL.WasCalled("DropSchema") {
		t.Error("should NOT drop schema when it has objects")
	}
}

// =============================================================================
// Critère 17: Deletion + connexion perdue → log + finalizer retiré
// =============================================================================

func TestSchemaReconcile_DeletionConnectionLost(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest

	policy := v1alpha1.DeletionPolicyDelete
	schema := testSchema("myschema", nil, &policy)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myschema"))

	var current v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &current)
	current.Finalizers = []string{v1alpha1.Finalizer}

	_, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("deletion should not block on connection error: %v", err)
	}
	// Finalizer should be removed (via Update in handleDeletion)
}

// =============================================================================
// Critère 18: Deletion + DROP échoue → log + finalizer retiré
// =============================================================================

func TestSchemaReconcile_DeletionDropFails(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.SetMethodError("DropSchema", fmt.Errorf("drop failed"))
	mockSQL.ResetCalls()

	policy := v1alpha1.DeletionPolicyDelete
	schema := testSchema("myschema", nil, &policy)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myschema"))

	var current v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &current)
	current.Finalizers = []string{v1alpha1.Finalizer}

	_, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("deletion should not block on drop error: %v", err)
	}
}

// =============================================================================
// Critère 19: Pas de finalizer + DeletionTimestamp → retour immédiat
// =============================================================================

func TestSchemaReconcile_DeletionNoFinalizer(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()

	schema := testSchema("myschema", nil, nil)
	// No finalizer set
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	var current v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &current)

	result, err := r.handleDeletion(context.Background(), &current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 || result.Requeue {
		t.Error("expected immediate return with no requeue")
	}
}

// =============================================================================
// Critère 1 bis: CR not found → no error
// =============================================================================

func TestSchemaReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestSchemaReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Error("expected no requeue for not-found CR")
	}
}
