package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

func newTestDatabaseUserReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*DatabaseUserReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.DatabaseUser{}).
		WithRuntimeObjects(objs...).Build()
	recorder := record.NewFakeRecorder(20)

	r := &DatabaseUserReconciler{
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

func readyLogin() *v1alpha1.Login {
	port := int32(1433)
	return &v1alpha1.Login{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "mylogin-cr",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.LoginSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			LoginName:      "mylogin_sql",
			PasswordSecret: v1alpha1.SecretReference{Name: "login-password"},
		},
		Status: v1alpha1.LoginStatus{
			Conditions: []metav1.Condition{
				{
					Type:   v1alpha1.ConditionReady,
					Status: metav1.ConditionTrue,
					Reason: v1alpha1.ReasonReady,
				},
			},
		},
	}
}

func testDatabaseUser(name string) *v1alpha1.DatabaseUser {
	port := int32(1433)
	return &v1alpha1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.DatabaseUserSpec{
			Server: v1alpha1.ServerReference{
				Host:              "mssql.svc",
				Port:              &port,
				CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"},
			},
			DatabaseName:  "mydb",
			UserName:      "myuser",
			LoginRef:      v1alpha1.LoginReference{Name: "mylogin-cr"},
			DatabaseRoles: []string{"db_datareader"},
		},
	}
}

// --- Test: Not Found ---
func TestDatabaseUserReconcile_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestDatabaseUserReconciler(nil, mockSQL)
	result, err := r.Reconcile(context.Background(), reqFor("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

// --- Test: LoginRef not found → Ready=False ---
func TestDatabaseUserReconcile_LoginRefNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	dbUser := testDatabaseUser("myuser-cr")
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("myuser-cr"))
	if err != nil {
		t.Fatalf("permanent error should not be returned: %v", err)
	}

	var updated v1alpha1.DatabaseUser
	r.Client.Get(context.Background(), reqFor("myuser-cr").NamespacedName, &updated)

	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ReasonLoginRefNotFound {
			found = true
		}
	}
	if !found {
		t.Error("expected Ready=False with Reason=LoginRefNotFound")
	}
}

// --- Test: Happy path creation → Ready=True ---
func TestDatabaseUserReconcile_Creation(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateDatabase(context.Background(), "mydb", nil)
	mockSQL.CreateLogin(context.Background(), "mylogin_sql", "pass")

	dbUser := testDatabaseUser("myuser-cr")
	login := readyLogin()
	r, recorder := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("myuser-cr"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockSQL.WasCalled("CreateUser") {
		t.Error("expected CreateUser to be called")
	}

	// Verify roles
	roles, _ := mockSQL.GetUserDatabaseRoles(context.Background(), "mydb", "myuser")
	if len(roles) != 1 || roles[0] != "db_datareader" {
		t.Errorf("expected [db_datareader], got %v", roles)
	}

	// Finalizer
	var updated v1alpha1.DatabaseUser
	r.Client.Get(context.Background(), reqFor("myuser-cr").NamespacedName, &updated)
	hasFinalizer := false
	for _, f := range updated.Finalizers {
		if f == v1alpha1.Finalizer {
			hasFinalizer = true
		}
	}
	if !hasFinalizer {
		t.Error("expected finalizer")
	}

	select {
	case <-recorder.Events:
	default:
		t.Error("expected event")
	}
}

// --- Test: Idempotence ---
func TestDatabaseUserReconcile_Idempotence(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateDatabase(context.Background(), "mydb", nil)
	mockSQL.CreateLogin(context.Background(), "mylogin_sql", "pass")

	dbUser := testDatabaseUser("myuser-cr")
	login := readyLogin()
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myuser-cr"))
	mockSQL.ResetCalls()

	_, err := r.Reconcile(context.Background(), reqFor("myuser-cr"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockSQL.WasCalled("CreateUser") {
		t.Error("expected CreateUser NOT to be called on second reconcile")
	}
}

// --- Test: Role addition ---
func TestDatabaseUserReconcile_RoleAdd(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateDatabase(context.Background(), "mydb", nil)
	mockSQL.CreateLogin(context.Background(), "mylogin_sql", "pass")

	dbUser := testDatabaseUser("myuser-cr")
	login := readyLogin()
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	// First reconcile
	r.Reconcile(context.Background(), reqFor("myuser-cr"))

	// Add a role
	var current v1alpha1.DatabaseUser
	r.Client.Get(context.Background(), reqFor("myuser-cr").NamespacedName, &current)
	current.Spec.DatabaseRoles = []string{"db_datareader", "db_datawriter"}
	r.Client.Update(context.Background(), &current)

	mockSQL.ResetCalls()
	_, err := r.Reconcile(context.Background(), reqFor("myuser-cr"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	roles, _ := mockSQL.GetUserDatabaseRoles(context.Background(), "mydb", "myuser")
	if len(roles) != 2 {
		t.Errorf("expected 2 roles, got %v", roles)
	}
}

// --- Test: Role removal ---
func TestDatabaseUserReconcile_RoleRemove(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateDatabase(context.Background(), "mydb", nil)
	mockSQL.CreateLogin(context.Background(), "mylogin_sql", "pass")

	dbUser := testDatabaseUser("myuser-cr")
	dbUser.Spec.DatabaseRoles = []string{"db_datareader", "db_datawriter"}
	login := readyLogin()
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myuser-cr"))

	// Remove db_datawriter
	var current v1alpha1.DatabaseUser
	r.Client.Get(context.Background(), reqFor("myuser-cr").NamespacedName, &current)
	current.Spec.DatabaseRoles = []string{"db_datareader"}
	r.Client.Update(context.Background(), &current)

	mockSQL.ResetCalls()
	r.Reconcile(context.Background(), reqFor("myuser-cr"))

	if !mockSQL.WasCalled("RemoveUserFromDatabaseRole") {
		t.Error("expected RemoveUserFromDatabaseRole to be called")
	}

	roles, _ := mockSQL.GetUserDatabaseRoles(context.Background(), "mydb", "myuser")
	if len(roles) != 1 || roles[0] != "db_datareader" {
		t.Errorf("expected [db_datareader], got %v", roles)
	}
}

// --- Test: Secret not found ---
func TestDatabaseUserReconcile_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	dbUser := testDatabaseUser("myuser-cr")
	login := readyLogin()
	// No SA secret
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("myuser-cr"))
	if err != nil {
		t.Fatalf("permanent error should not be returned: %v", err)
	}

	var updated v1alpha1.DatabaseUser
	r.Client.Get(context.Background(), reqFor("myuser-cr").NamespacedName, &updated)
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

// --- Compile-time check for LoginRef resolution ---
func TestDatabaseUserReconcile_LoginRefResolvesLoginName(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateDatabase(context.Background(), "mydb", nil)
	mockSQL.CreateLogin(context.Background(), "mylogin_sql", "pass")

	dbUser := testDatabaseUser("myuser-cr")
	login := readyLogin()
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	r.Reconcile(context.Background(), reqFor("myuser-cr"))

	// The user should have been created with the LOGIN NAME from the Login CR spec,
	// not the CR name
	exists, _ := mockSQL.UserExists(context.Background(), "mydb", "myuser")
	if !exists {
		t.Error("expected user to exist")
	}

	// Verify the user was created with the SQL login name (mylogin_sql), not the CR name (mylogin-cr)
	user := mockSQL.GetMockUser("mydb", "myuser")
	if user == nil {
		t.Fatal("expected mock user to exist")
	}
	if user.LoginName != "mylogin_sql" {
		t.Errorf("expected LoginName 'mylogin_sql', got %q", user.LoginName)
	}
}

// Add a helper for tests to avoid lock issues
func init() {
	_ = &corev1.Secret{}
}
