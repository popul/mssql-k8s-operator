package controller

import (
	"context"
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

// --- Test: CR not found → no error ---
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

// --- Test: Happy path creation → Ready=True ---
func TestSchemaReconcile_Creation(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("myschema"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("CreateSchema") {
		t.Error("expected CreateSchema to be called")
	}

	// Second reconcile should add finalizer, then third actually reconciles
	result, err = r.Reconcile(context.Background(), reqFor("myschema"))
	if err != nil {
		t.Fatalf("unexpected error on second reconcile: %v", err)
	}

	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter for ready state")
	}

	var updated v1alpha1.Schema
	if err := r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &updated); err != nil {
		t.Fatalf("failed to get updated schema: %v", err)
	}

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

// --- Test: Schema already exists → no create, Ready=True ---
func TestSchemaReconcile_AlreadyExists(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-create the schema in the mock
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// First reconcile adds finalizer
	r.Reconcile(context.Background(), reqFor("myschema"))
	// Second reconcile reconciles
	_, err := r.Reconcile(context.Background(), reqFor("myschema"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("CreateSchema") {
		t.Error("should NOT call CreateSchema when schema already exists")
	}
}

// --- Test: Owner reconciliation ---
func TestSchemaReconcile_OwnerUpdate(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-create schema with owner "dbo"
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	owner := "appuser"
	schema := testSchema("myschema", &owner, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// First reconcile adds finalizer
	r.Reconcile(context.Background(), reqFor("myschema"))
	// Second reconcile should update owner
	_, err := r.Reconcile(context.Background(), reqFor("myschema"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("SetSchemaOwner") {
		t.Error("expected SetSchemaOwner to be called")
	}
}

// --- Test: Secret not found → Ready=False ---
func TestSchemaReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	// No saSecret() provided
	r, _ := newTestSchemaReconciler([]runtime.Object{schema}, mockSQL)

	// First reconcile adds finalizer
	r.Reconcile(context.Background(), reqFor("myschema"))
	// Second reconcile should fail to get credentials
	_, err := r.Reconcile(context.Background(), reqFor("myschema"))
	if err != nil {
		t.Fatalf("permanent error should not be returned: %v", err)
	}

	var updated v1alpha1.Schema
	r.Client.Get(context.Background(), reqFor("myschema").NamespacedName, &updated)

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

// --- Test: Connection error → transient error returned ---
func TestSchemaReconcile_ConnectionError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errTest
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// First reconcile adds finalizer
	r.Reconcile(context.Background(), reqFor("myschema"))
	// Second reconcile should fail to connect
	_, err := r.Reconcile(context.Background(), reqFor("myschema"))
	if err == nil {
		t.Error("expected transient error for connection failure")
	}
}

// --- Test: Deletion with Retain policy ---
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

// --- Test: Deletion with Delete policy ---
func TestSchemaReconcile_DeletionDelete(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.ResetCalls()

	policy := v1alpha1.DeletionPolicyDelete
	schema := testSchema("myschema", nil, &policy)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// Reconcile to add finalizer
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

// --- Test: Deletion blocked by objects ---
func TestSchemaReconcile_DeletionBlockedByObjects(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateSchema(context.Background(), "mydb", "app", nil)
	mockSQL.SetSchemaHasObjects("mydb", "app", true)
	mockSQL.ResetCalls()

	policy := v1alpha1.DeletionPolicyDelete
	schema := testSchema("myschema", nil, &policy)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// Reconcile to add finalizer
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

// --- Test: Idempotence ---
func TestSchemaReconcile_Idempotent(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// Reconcile multiple times
	for i := 0; i < 3; i++ {
		_, err := r.Reconcile(context.Background(), reqFor("myschema"))
		if err != nil {
			t.Fatalf("reconcile %d: unexpected error: %v", i, err)
		}
	}

	// CreateSchema should be called only once (when it didn't exist)
	if mockSQL.CallCount("CreateSchema") != 1 {
		t.Errorf("expected CreateSchema called once, got %d", mockSQL.CallCount("CreateSchema"))
	}
}

// --- Test: RequeueAfter has jitter ---
func TestSchemaReconcile_RequeueHasJitter(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	schema := testSchema("myschema", nil, nil)
	r, _ := newTestSchemaReconciler([]runtime.Object{schema, saSecret()}, mockSQL)

	// First reconcile adds finalizer
	r.Reconcile(context.Background(), reqFor("myschema"))

	seen := make(map[time.Duration]bool)
	for i := 0; i < 10; i++ {
		result, err := r.Reconcile(context.Background(), reqFor("myschema"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		seen[result.RequeueAfter] = true
	}
	if len(seen) < 2 {
		t.Error("expected jitter to produce varying RequeueAfter values")
	}
}
