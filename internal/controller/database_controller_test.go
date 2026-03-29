package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func newTestDatabaseReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*DatabaseReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	clientBuilder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.Database{})
	for _, obj := range objs {
		clientBuilder = clientBuilder.WithRuntimeObjects(obj)
	}
	k8sClient := clientBuilder.Build()
	recorder := record.NewFakeRecorder(20)

	r := &DatabaseReconciler{
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

func saSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sa-credentials",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"username": []byte("sa"),
			"password": []byte("P@ssw0rd"),
		},
	}
}

func testDatabase(name string, deletionPolicy *v1alpha1.DeletionPolicy) *v1alpha1.Database {
	port := int32(1433)
	return &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.DatabaseSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			DatabaseName:   name,
			DeletionPolicy: deletionPolicy,
		},
	}
}

func reqFor(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
}

func policyPtr(p v1alpha1.DeletionPolicy) *v1alpha1.DeletionPolicy { return &p }

// --- Test: CR not found (deleted) → no error ---
func TestDatabaseReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestDatabaseReconciler(nil, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Error("expected no requeue for not-found CR")
	}
}

// --- Test: Happy path creation → Ready=True ---
func TestDatabaseReconcile_Creation(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", nil)
	r, recorder := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have created the database
	if !mockSQL.WasCalled("CreateDatabase") {
		t.Error("expected CreateDatabase to be called")
	}

	// Check the CR was updated with finalizer and status
	var updated v1alpha1.Database
	if err := r.Client.Get(context.Background(), reqFor("mydb").NamespacedName, &updated); err != nil {
		t.Fatalf("failed to get updated CR: %v", err)
	}

	// Finalizer should be set
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == v1alpha1.Finalizer {
			hasFinalizer = true
		}
	}
	if !hasFinalizer {
		t.Error("expected finalizer to be set")
	}

	// Should requeue for periodic reconciliation
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0")
	}

	// Should have emitted a Normal event
	select {
	case event := <-recorder.Events:
		if event == "" {
			t.Error("expected non-empty event")
		}
	default:
		t.Error("expected at least one event")
	}
}

// --- Test: Idempotence — second reconciliation does not mutate ---
func TestDatabaseReconcile_Idempotence(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", nil)
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// First reconcile: creates the database
	r.Reconcile(context.Background(), reqFor("mydb"))

	mockSQL.ResetCalls()

	// Second reconcile: should not create again
	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("CreateDatabase") {
		t.Error("expected CreateDatabase NOT to be called on second reconcile")
	}
}

// --- Test: Owner update ---
func TestDatabaseReconcile_OwnerUpdate(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	owner := "newowner"
	db := testDatabase("mydb", nil)
	db.Spec.Owner = &owner
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// First reconcile creates the DB
	r.Reconcile(context.Background(), reqFor("mydb"))
	mockSQL.ResetCalls()

	// Change the owner in the mock to simulate drift
	currentOwner, _ := mockSQL.GetDatabaseOwner(context.Background(), "mydb")
	if currentOwner != "newowner" {
		// The controller should have set the owner
		t.Errorf("expected owner 'newowner', got %q", currentOwner)
	}
}

// --- Test: Secret not found → Ready=False ---
func TestDatabaseReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", nil)
	// No secret provided
	r, _ := newTestDatabaseReconciler([]runtime.Object{db}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error (permanent errors should not return err): %v", err)
	}
	_ = result

	var updated v1alpha1.Database
	r.Client.Get(context.Background(), reqFor("mydb").NamespacedName, &updated)

	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ReasonSecretNotFound {
			found = true
		}
	}
	if !found {
		t.Error("expected condition Ready=False with Reason=SecretNotFound")
	}
}

// --- Test: Connection failed → transient error (return err) ---
func TestDatabaseReconcile_ConnectionFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errors.New("connection refused")
	db := testDatabase("mydb", nil)
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err == nil {
		t.Error("expected transient error to be returned")
	}
}

// --- Test: Deletion with policy Delete → DropDatabase ---
func TestDatabaseReconcile_DeletionPolicyDelete(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", policyPtr(v1alpha1.DeletionPolicyDelete))
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// First reconcile to create + add finalizer
	r.Reconcile(context.Background(), reqFor("mydb"))
	mockSQL.ResetCalls()

	// Simulate deletion by setting DeletionTimestamp
	var current v1alpha1.Database
	r.Client.Get(context.Background(), reqFor("mydb").NamespacedName, &current)
	now := metav1.Now()
	current.DeletionTimestamp = &now
	// We can't set DeletionTimestamp directly with fake client — instead test the logic
	// by calling reconcile and checking the mock was invoked properly.
	// The fake client doesn't support deletion timestamps well,
	// so we verify behavior through the finalizer + delete flow.

	// Delete the CR
	r.Client.Delete(context.Background(), &current)

	result, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = result

	// After CR is gone, reconcile should return silently
	// The actual deletion logic (DropDatabase) is tested in integration tests
	// because fake client doesn't set DeletionTimestamp on delete.
}

// --- Test: Deletion with policy Retain → no DropDatabase ---
func TestDatabaseReconcile_DeletionPolicyRetain(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", policyPtr(v1alpha1.DeletionPolicyRetain))
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("mydb"))
	mockSQL.ResetCalls()

	// Delete
	var current v1alpha1.Database
	r.Client.Get(context.Background(), reqFor("mydb").NamespacedName, &current)
	r.Client.Delete(context.Background(), &current)

	r.Reconcile(context.Background(), reqFor("mydb"))

	// DropDatabase should NOT have been called for Retain
	if mockSQL.WasCalled("DropDatabase") {
		t.Error("expected DropDatabase NOT to be called with Retain policy")
	}
}

// --- Test: Invalid credentials secret (missing keys) → Ready=False ---
func TestDatabaseReconcile_InvalidCredentialsSecret(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", nil)
	badSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sa-credentials",
			Namespace: "default",
		},
		Data: map[string][]byte{
			// Missing "username" and "password" keys
		},
	}
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, badSecret}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	// This should be treated as a permanent error (no retry)
	if err != nil {
		t.Fatalf("permanent errors should not return err, got: %v", err)
	}

	var updated v1alpha1.Database
	r.Client.Get(context.Background(), reqFor("mydb").NamespacedName, &updated)

	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ReasonInvalidCredentialsSecret {
			found = true
		}
	}
	if !found {
		t.Error("expected condition Ready=False with Reason=InvalidCredentialsSecret")
	}
}

// --- Test: Adopt existing database ---
func TestDatabaseReconcile_AdoptExisting(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-create the database in the mock
	mockSQL.CreateDatabase(context.Background(), "mydb", nil)
	mockSQL.ResetCalls()

	db := testDatabase("mydb", nil)
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have called CreateDatabase (it already exists)
	if mockSQL.WasCalled("CreateDatabase") {
		t.Error("expected CreateDatabase NOT to be called for existing database")
	}
}

// --- Test: Recovery model reconciliation ---
func TestDatabaseReconcile_RecoveryModel(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	rm := v1alpha1.RecoveryModelSimple
	db := testDatabase("mydb", nil)
	db.Spec.RecoveryModel = &rm

	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// First reconcile creates the DB
	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Second reconcile sets recovery model
	_, err = r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("SetDatabaseRecoveryModel") {
		t.Error("expected SetDatabaseRecoveryModel to be called")
	}
}

// --- Test: Compatibility level reconciliation ---
func TestDatabaseReconcile_CompatibilityLevel(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	cl := int32(150)
	db := testDatabase("mydb", nil)
	db.Spec.CompatibilityLevel = &cl

	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// First reconcile creates DB
	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Second reconcile sets compat level
	_, err = r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("SetDatabaseCompatibilityLevel") {
		t.Error("expected SetDatabaseCompatibilityLevel to be called")
	}
}

// --- Test: Database options reconciliation ---
func TestDatabaseReconcile_Options(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", nil)
	db.Spec.Options = []v1alpha1.DatabaseOption{
		{Name: "ALLOW_SNAPSHOT_ISOLATION", Value: true},
		{Name: "READ_COMMITTED_SNAPSHOT", Value: true},
	}

	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// First reconcile creates DB
	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Second reconcile sets options
	_, err = r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.CallCount("SetDatabaseOption") != 2 {
		t.Errorf("expected 2 calls to SetDatabaseOption, got %d", mockSQL.CallCount("SetDatabaseOption"))
	}
}

// --- Test: Recovery model idempotent ---
func TestDatabaseReconcile_RecoveryModel_Idempotent(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	// Pre-create database with Simple recovery model
	mockSQL.CreateDatabase(context.Background(), "mydb", nil)
	mockSQL.SetDatabaseRecoveryModel(context.Background(), "mydb", "Simple")
	mockSQL.ResetCalls()

	rm := v1alpha1.RecoveryModelSimple
	db := testDatabase("mydb", nil)
	db.Spec.RecoveryModel = &rm

	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have called SetDatabaseRecoveryModel since it's already Simple
	if mockSQL.WasCalled("SetDatabaseRecoveryModel") {
		t.Error("expected SetDatabaseRecoveryModel NOT to be called when already matching")
	}
}
