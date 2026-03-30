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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

func newTestLoginReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*LoginReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Login{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &LoginReconciler{
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

func passwordSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "login-password",
			Namespace:       "default",
			ResourceVersion: "100",
		},
		Data: map[string][]byte{
			"password": []byte("InitialP@ss"),
		},
	}
}

func testLogin(name string, policy *v1alpha1.DeletionPolicy) *v1alpha1.Login {
	port := int32(1433)
	return &v1alpha1.Login{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.LoginSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			LoginName:      name,
			PasswordSecret: v1alpha1.SecretReference{Name: "login-password"},
			DeletionPolicy: policy,
		},
	}
}

// --- Test: Not Found ---
func TestLoginReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestLoginReconciler(nil, mockSQL)
	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

// --- Test: Happy path creation → Ready=True ---
func TestLoginReconcile_Creation(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	r, recorder := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("CreateLogin") {
		t.Error("expected CreateLogin to be called")
	}

	var updated v1alpha1.Login
	r.Client.Get(context.Background(), reqFor("mylogin").NamespacedName, &updated)

	// Finalizer
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == v1alpha1.Finalizer {
			hasFinalizer = true
		}
	}
	if !hasFinalizer {
		t.Error("expected finalizer")
	}

	// PasswordSecretResourceVersion tracked
	if updated.Status.PasswordSecretResourceVersion == "" {
		t.Error("expected PasswordSecretResourceVersion to be set")
	}

	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0")
	}

	select {
	case <-recorder.Events:
	default:
		t.Error("expected event")
	}
}

// --- Test: Idempotence ---
func TestLoginReconcile_Idempotence(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("mylogin"))
	mockSQL.ResetCalls()

	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockSQL.WasCalled("CreateLogin") {
		t.Error("expected CreateLogin NOT to be called")
	}
}

// --- Test: Password rotation ---
func TestLoginReconcile_PasswordRotation(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	// Start with ResourceVersion "100"
	pwSecret := passwordSecret()
	r, recorder := newTestLoginReconciler([]runtime.Object{login, saSecret(), pwSecret}, mockSQL)

	// First reconcile — creates login, records ResourceVersion
	r.Reconcile(context.Background(), reqFor("mylogin"))

	// Get the current login status to see what ResourceVersion was recorded
	var current v1alpha1.Login
	r.Client.Get(context.Background(), reqFor("mylogin").NamespacedName, &current)
	firstRV := current.Status.PasswordSecretResourceVersion

	mockSQL.ResetCalls()

	// Simulate password change: delete and recreate the Secret with new data
	// (fake client auto-increments ResourceVersion)
	r.Client.Delete(context.Background(), pwSecret)
	newPwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "login-password",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"password": []byte("NewP@ss456"),
		},
	}
	r.Client.Create(context.Background(), newPwSecret)

	// Verify ResourceVersion changed
	var updatedSecret corev1.Secret
	r.Client.Get(context.Background(), types.NamespacedName{Name: "login-password", Namespace: "default"}, &updatedSecret)
	if updatedSecret.ResourceVersion == firstRV {
		t.Fatalf("ResourceVersion should have changed, still %q", firstRV)
	}

	// Second reconcile should detect password change
	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("UpdateLoginPassword") {
		t.Error("expected UpdateLoginPassword to be called")
	}

	// Check for password rotation event
	found := false
	for len(recorder.Events) > 0 {
		event := <-recorder.Events
		if event != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected password rotation event")
	}
}

// --- Test: Server roles added ---
func TestLoginReconcile_ServerRolesAdd(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	login.Spec.ServerRoles = []string{"dbcreator", "securityadmin"}
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	roles, _ := mockSQL.GetLoginServerRoles(context.Background(), "mylogin")
	if len(roles) != 2 {
		t.Errorf("expected 2 roles, got %d: %v", len(roles), roles)
	}
}

// --- Test: Server roles removed ---
func TestLoginReconcile_ServerRolesRemove(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	login.Spec.ServerRoles = []string{"dbcreator", "securityadmin"}
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	// First reconcile: add both roles
	r.Reconcile(context.Background(), reqFor("mylogin"))
	mockSQL.ResetCalls()

	// Remove securityadmin from spec
	var current v1alpha1.Login
	r.Client.Get(context.Background(), reqFor("mylogin").NamespacedName, &current)
	current.Spec.ServerRoles = []string{"dbcreator"}
	r.Client.Update(context.Background(), &current)

	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("RemoveLoginFromServerRole") {
		t.Error("expected RemoveLoginFromServerRole to be called")
	}

	roles, _ := mockSQL.GetLoginServerRoles(context.Background(), "mylogin")
	if len(roles) != 1 || roles[0] != "dbcreator" {
		t.Errorf("expected [dbcreator], got %v", roles)
	}
}

// --- Test: Connection failed → transient error ---
func TestLoginReconcile_ConnectionFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errors.New("connection refused")
	login := testLogin("mylogin", nil)
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err == nil {
		t.Error("expected transient error")
	}
}

// --- Test: Secret not found ---
func TestLoginReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	r, _ := newTestLoginReconciler([]runtime.Object{login}, mockSQL) // no secrets

	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("permanent error should not be returned: %v", err)
	}

	var updated v1alpha1.Login
	r.Client.Get(context.Background(), reqFor("mylogin").NamespacedName, &updated)

	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionFalse {
			found = true
		}
	}
	if !found {
		t.Error("expected Ready=False")
	}
}

// --- Test: Default database ---
func TestLoginReconcile_DefaultDatabase(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	defaultDB := "mydb"
	login.Spec.DefaultDatabase = &defaultDB
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	db, _ := mockSQL.GetLoginDefaultDatabase(context.Background(), "mylogin")
	if db != "mydb" {
		t.Errorf("expected default database 'mydb', got %q", db)
	}
}
