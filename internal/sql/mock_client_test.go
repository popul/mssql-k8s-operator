package sql

import (
	"context"
	"errors"
	"testing"
)

func TestMockClientImplementsInterface(t *testing.T) {
	var _ SQLClient = NewMockClient()
}

func TestMockClientDatabaseLifecycle(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()

	// Database does not exist initially
	exists, err := m.DatabaseExists(ctx, "testdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected database to not exist")
	}

	// Create database
	if err := m.CreateDatabase(ctx, "testdb", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Now it exists
	exists, err = m.DatabaseExists(ctx, "testdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected database to exist")
	}

	// Drop database
	if err := m.DropDatabase(ctx, "testdb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No longer exists
	exists, _ = m.DatabaseExists(ctx, "testdb")
	if exists {
		t.Error("expected database to not exist after drop")
	}

	// Drop again is idempotent
	if err := m.DropDatabase(ctx, "testdb"); err != nil {
		t.Fatalf("drop idempotent failed: %v", err)
	}
}

func TestMockClientDatabaseCollation(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()

	collation := "SQL_Latin1_General_CP1_CI_AS"
	if err := m.CreateDatabase(ctx, "testdb", &collation); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := m.GetDatabaseCollation(ctx, "testdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != collation {
		t.Errorf("expected collation %q, got %q", collation, got)
	}
}

func TestMockClientDatabaseOwner(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()

	m.CreateDatabase(ctx, "testdb", nil)

	if err := m.SetDatabaseOwner(ctx, "testdb", "myowner"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	owner, err := m.GetDatabaseOwner(ctx, "testdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myowner" {
		t.Errorf("expected owner 'myowner', got %q", owner)
	}
}

func TestMockClientLoginLifecycle(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()

	exists, _ := m.LoginExists(ctx, "testlogin")
	if exists {
		t.Error("expected login to not exist")
	}

	if err := m.CreateLogin(ctx, "testlogin", "password123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exists, _ = m.LoginExists(ctx, "testlogin")
	if !exists {
		t.Error("expected login to exist")
	}

	// Update password (should not error)
	if err := m.UpdateLoginPassword(ctx, "testlogin", "newpassword"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drop
	if err := m.DropLogin(ctx, "testlogin"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	exists, _ = m.LoginExists(ctx, "testlogin")
	if exists {
		t.Error("expected login to not exist after drop")
	}

	// Drop again is idempotent
	if err := m.DropLogin(ctx, "testlogin"); err != nil {
		t.Fatalf("drop idempotent failed: %v", err)
	}
}

func TestMockClientLoginRoles(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()
	m.CreateLogin(ctx, "testlogin", "pass")

	if err := m.AddLoginToServerRole(ctx, "testlogin", "dbcreator"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	roles, err := m.GetLoginServerRoles(ctx, "testlogin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roles) != 1 || roles[0] != "dbcreator" {
		t.Errorf("expected [dbcreator], got %v", roles)
	}

	if err := m.RemoveLoginFromServerRole(ctx, "testlogin", "dbcreator"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	roles, _ = m.GetLoginServerRoles(ctx, "testlogin")
	if len(roles) != 0 {
		t.Errorf("expected empty roles, got %v", roles)
	}
}

func TestMockClientLoginDefaultDatabase(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()
	m.CreateLogin(ctx, "testlogin", "pass")

	if err := m.SetLoginDefaultDatabase(ctx, "testlogin", "mydb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	db, err := m.GetLoginDefaultDatabase(ctx, "testlogin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db != "mydb" {
		t.Errorf("expected 'mydb', got %q", db)
	}
}

func TestMockClientUserLifecycle(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()
	m.CreateDatabase(ctx, "testdb", nil)
	m.CreateLogin(ctx, "testlogin", "pass")

	exists, _ := m.UserExists(ctx, "testdb", "testuser")
	if exists {
		t.Error("expected user to not exist")
	}

	if err := m.CreateUser(ctx, "testdb", "testuser", "testlogin"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exists, _ = m.UserExists(ctx, "testdb", "testuser")
	if !exists {
		t.Error("expected user to exist")
	}

	if err := m.DropUser(ctx, "testdb", "testuser"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	exists, _ = m.UserExists(ctx, "testdb", "testuser")
	if exists {
		t.Error("expected user to not exist after drop")
	}
}

func TestMockClientUserRoles(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()
	m.CreateDatabase(ctx, "testdb", nil)
	m.CreateLogin(ctx, "testlogin", "pass")
	m.CreateUser(ctx, "testdb", "testuser", "testlogin")

	m.AddUserToDatabaseRole(ctx, "testdb", "testuser", "db_datareader")
	roles, _ := m.GetUserDatabaseRoles(ctx, "testdb", "testuser")
	if len(roles) != 1 || roles[0] != "db_datareader" {
		t.Errorf("expected [db_datareader], got %v", roles)
	}

	m.RemoveUserFromDatabaseRole(ctx, "testdb", "testuser", "db_datareader")
	roles, _ = m.GetUserDatabaseRoles(ctx, "testdb", "testuser")
	if len(roles) != 0 {
		t.Errorf("expected empty, got %v", roles)
	}
}

func TestMockClientUserOwnsObjects(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()
	m.CreateDatabase(ctx, "testdb", nil)
	m.CreateLogin(ctx, "testlogin", "pass")
	m.CreateUser(ctx, "testdb", "testuser", "testlogin")

	owns, _ := m.UserOwnsObjects(ctx, "testdb", "testuser")
	if owns {
		t.Error("expected user to not own objects by default")
	}

	// Simulate ownership
	m.SetUserOwnsObjects("testdb", "testuser", true)
	owns, _ = m.UserOwnsObjects(ctx, "testdb", "testuser")
	if !owns {
		t.Error("expected user to own objects")
	}
}

func TestMockClientLoginHasUsers(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()
	m.CreateLogin(ctx, "testlogin", "pass")

	has, _ := m.LoginHasUsers(ctx, "testlogin")
	if has {
		t.Error("expected login to have no users")
	}

	m.CreateDatabase(ctx, "testdb", nil)
	m.CreateUser(ctx, "testdb", "testuser", "testlogin")

	has, _ = m.LoginHasUsers(ctx, "testlogin")
	if !has {
		t.Error("expected login to have users")
	}
}

func TestMockClientConnectError(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()
	m.ConnectError = errors.New("connection refused")

	err := m.Ping(ctx)
	if err == nil {
		t.Error("expected ping error")
	}

	_, err = m.DatabaseExists(ctx, "testdb")
	if err == nil {
		t.Error("expected error on DatabaseExists when ConnectError is set")
	}
}

func TestMockClientCallTracking(t *testing.T) {
	ctx := context.Background()
	m := NewMockClient()

	m.CreateDatabase(ctx, "testdb", nil)
	m.CreateLogin(ctx, "testlogin", "pass")

	if !m.WasCalled("CreateDatabase") {
		t.Error("expected CreateDatabase to be tracked")
	}
	if !m.WasCalled("CreateLogin") {
		t.Error("expected CreateLogin to be tracked")
	}
	if m.WasCalled("DropDatabase") {
		t.Error("expected DropDatabase to NOT be tracked")
	}

	m.ResetCalls()
	if m.WasCalled("CreateDatabase") {
		t.Error("expected calls to be reset")
	}
}

func TestMockClientClose(t *testing.T) {
	m := NewMockClient()
	if err := m.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
