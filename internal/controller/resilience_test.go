package controller

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// =============================================================================
// Database Controller — Resilience & Deletion Tests
// =============================================================================

func TestDatabaseReconcile_CreateDatabaseTransientError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("CreateDatabase", fmt.Errorf("deadlock victim"))
	db := testDatabase("mydb", nil)
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err == nil {
		t.Fatal("expected transient error to be returned for retry")
	}
	if mockSQL.CallCount("CreateDatabase") != 1 {
		t.Error("expected CreateDatabase to be attempted")
	}
}

func TestDatabaseReconcile_SetOwnerFailsAfterCreate(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	owner := "newowner"
	db := testDatabase("mydb", nil)
	db.Spec.Owner = &owner
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// Inject SetDatabaseOwner error from the start — CreateDatabase succeeds, but owner change fails
	mockSQL.SetMethodError("SetDatabaseOwner", fmt.Errorf("permission denied"))

	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err == nil {
		t.Fatal("expected error when SetDatabaseOwner fails")
	}
	if !mockSQL.WasCalled("CreateDatabase") {
		t.Error("expected CreateDatabase to be called")
	}
	if !mockSQL.WasCalled("SetDatabaseOwner") {
		t.Error("expected SetDatabaseOwner to be attempted")
	}
}

func TestDatabaseReconcile_DeletionDropFails_FinalizerStillRemoved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("DropDatabase", fmt.Errorf("cannot drop database"))
	policy := v1alpha1.DeletionPolicyDelete
	db := testDatabase("mydb", &policy)
	db.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	db.DeletionTimestamp = &now

	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("deletion should not return error even if drop fails: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue after deletion")
	}
}

func TestDatabaseReconcile_DeletionConnectionFails_FinalizerStillRemoved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = fmt.Errorf("connection refused")
	policy := v1alpha1.DeletionPolicyDelete
	db := testDatabase("mydb", &policy)
	db.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	db.DeletionTimestamp = &now

	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("deletion should not block on connection failure: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue")
	}
}

// =============================================================================
// Login Controller — Resilience & Deletion Tests
// =============================================================================

func TestLoginReconcile_CreateLoginTransientError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("CreateLogin", fmt.Errorf("timeout expired"))
	login := testLogin("mylogin", nil)
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err == nil {
		t.Fatal("expected transient error for retry")
	}
}

func TestLoginReconcile_PasswordUpdateFails(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	// First reconcile: create login
	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Inject error on password update, simulate secret version change
	mockSQL.SetMethodError("UpdateLoginPassword", fmt.Errorf("ALTER LOGIN failed"))

	var updated v1alpha1.Login
	r.Client.Get(context.Background(), reqFor("mylogin").NamespacedName, &updated)
	updated.Status.PasswordSecretResourceVersion = "old-version"
	r.Status().Update(context.Background(), &updated)

	_, err = r.Reconcile(context.Background(), reqFor("mylogin"))
	if err == nil {
		t.Fatal("expected error when password update fails")
	}
}

func TestLoginReconcile_DeletionLoginHasUsers_Requeues(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateLogin(context.Background(), "mylogin", "pass")
	mockSQL.CreateUser(context.Background(), "mydb", "myuser", "mylogin")

	policy := v1alpha1.DeletionPolicyDelete
	login := testLogin("mylogin", &policy)
	login.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	login.DeletionTimestamp = &now

	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("should not return error, should requeue: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 when login has users")
	}
	if mockSQL.WasCalled("DropLogin") {
		t.Error("DropLogin should not be called when login has users")
	}
}

func TestLoginReconcile_DeletionDropFails_FinalizerStillRemoved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("DropLogin", fmt.Errorf("cannot drop login"))
	policy := v1alpha1.DeletionPolicyDelete
	login := testLogin("mylogin", &policy)
	login.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	login.DeletionTimestamp = &now

	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("deletion should not return error even if drop fails: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue after deletion")
	}
}

func TestLoginReconcile_DeletionConnectionFails_FinalizerStillRemoved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = fmt.Errorf("connection refused")
	policy := v1alpha1.DeletionPolicyDelete
	login := testLogin("mylogin", &policy)
	login.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	login.DeletionTimestamp = &now

	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("deletion should not block on connection failure: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue")
	}
}

// =============================================================================
// DatabaseUser Controller — Resilience & Deletion Tests
// =============================================================================

func TestDatabaseUserReconcile_CreateUserTransientError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("CreateUser", fmt.Errorf("deadlock victim"))
	login := testLogin("mylogin-cr", nil)
	dbUser := testDatabaseUser("myuser")
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("myuser"))
	if err == nil {
		t.Fatal("expected transient error for retry")
	}
}

func TestDatabaseUserReconcile_RoleAddFails(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin-cr", nil)
	dbUser := testDatabaseUser("myuser")
	dbUser.Spec.DatabaseRoles = []string{"db_datareader"}
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	mockSQL.SetMethodError("AddUserToDatabaseRole", fmt.Errorf("role not found"))

	_, err := r.Reconcile(context.Background(), reqFor("myuser"))
	if err == nil {
		t.Fatal("expected error when role add fails")
	}
}

func TestDatabaseUserReconcile_DeletionUserOwnsObjects_Requeues(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.CreateUser(context.Background(), "mydb", "myuser", "mylogin")
	mockSQL.SetUserOwnsObjects("mydb", "myuser", true)

	login := testLogin("mylogin-cr", nil)
	dbUser := testDatabaseUser("myuser")
	dbUser.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	dbUser.DeletionTimestamp = &now

	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("myuser"))
	if err != nil {
		t.Fatalf("should not return error, should requeue: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 when user owns objects")
	}
	if mockSQL.WasCalled("DropUser") {
		t.Error("DropUser should not be called when user owns objects")
	}
}

func TestDatabaseUserReconcile_DeletionDropFails_FinalizerStillRemoved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.SetMethodError("DropUser", fmt.Errorf("cannot drop user"))
	login := testLogin("mylogin-cr", nil)
	dbUser := testDatabaseUser("myuser")
	dbUser.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	dbUser.DeletionTimestamp = &now

	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("myuser"))
	if err != nil {
		t.Fatalf("deletion should not return error even if drop fails: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue after deletion")
	}
}

func TestDatabaseUserReconcile_DeletionConnectionFails_FinalizerStillRemoved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = fmt.Errorf("connection refused")
	login := testLogin("mylogin-cr", nil)
	dbUser := testDatabaseUser("myuser")
	dbUser.Finalizers = []string{v1alpha1.Finalizer}
	now := metav1.Now()
	dbUser.DeletionTimestamp = &now

	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), reqFor("myuser"))
	if err != nil {
		t.Fatalf("deletion should not block on connection failure: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue")
	}
}

// =============================================================================
// Recovery Tests — Transient error then success
// =============================================================================

func TestDatabaseReconcile_RecoveryAfterTransientError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", nil)
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	mockSQL.SetMethodError("CreateDatabase", fmt.Errorf("deadlock"))
	_, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err == nil {
		t.Fatal("expected error")
	}

	mockSQL.SetMethodError("CreateDatabase", nil)
	result, err := r.Reconcile(context.Background(), reqFor("mydb"))
	if err != nil {
		t.Fatalf("expected recovery, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 after successful reconcile")
	}
}

func TestLoginReconcile_RecoveryAfterTransientError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin", nil)
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	mockSQL.SetMethodError("CreateLogin", fmt.Errorf("timeout"))
	_, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err == nil {
		t.Fatal("expected error")
	}

	mockSQL.SetMethodError("CreateLogin", nil)
	result, err := r.Reconcile(context.Background(), reqFor("mylogin"))
	if err != nil {
		t.Fatalf("expected recovery, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0")
	}
}

func TestDatabaseUserReconcile_RecoveryAfterTransientError(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("mylogin-cr", nil)
	dbUser := testDatabaseUser("myuser")
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	mockSQL.SetMethodError("CreateUser", fmt.Errorf("deadlock"))
	_, err := r.Reconcile(context.Background(), reqFor("myuser"))
	if err == nil {
		t.Fatal("expected error")
	}

	mockSQL.SetMethodError("CreateUser", nil)
	result, err := r.Reconcile(context.Background(), reqFor("myuser"))
	if err != nil {
		t.Fatalf("expected recovery, got: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0")
	}
}
