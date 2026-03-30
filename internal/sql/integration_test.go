//go:build integration

package sql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

// Integration tests run against a real SQL Server instance.
// Run with: go test -tags=integration ./internal/sql/...
//
// Prerequisites:
//   docker run -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD='P@ssw0rd123' \
//     -p 1433:1433 --name mssql-test -d mcr.microsoft.com/mssql/server:2022-latest
//
// Or set MSSQL_TEST_HOST, MSSQL_TEST_PORT, MSSQL_TEST_USER, MSSQL_TEST_PASSWORD env vars.

var (
	testHost     = getEnv("MSSQL_TEST_HOST", "localhost")
	testPort     = 1433
	testUser     = getEnv("MSSQL_TEST_USER", "sa")
	testPassword = getEnv("MSSQL_TEST_PASSWORD", "P@ssw0rd123")
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func setupTestClient(t *testing.T) SQLClient {
	t.Helper()
	factory := NewClientFactory()
	client, err := factory(testHost, testPort, testUser, testPassword, false)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Skipf("SQL Server not available: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()%100000)
}

// --- Database integration tests ---

func TestIntegration_DatabaseLifecycle(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	dbName := uniqueName("testdb")

	// Should not exist initially
	exists, err := client.DatabaseExists(ctx, dbName)
	if err != nil {
		t.Fatalf("DatabaseExists: %v", err)
	}
	if exists {
		t.Fatal("database should not exist yet")
	}

	// Create
	if err := client.CreateDatabase(ctx, dbName, nil); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	// Should exist now
	exists, err = client.DatabaseExists(ctx, dbName)
	if err != nil {
		t.Fatalf("DatabaseExists after create: %v", err)
	}
	if !exists {
		t.Fatal("database should exist after creation")
	}

	// Idempotence: creating again should fail (but not panic)
	err = client.CreateDatabase(ctx, dbName, nil)
	if err == nil {
		t.Fatal("expected error creating duplicate database")
	}

	// Drop
	if err := client.DropDatabase(ctx, dbName); err != nil {
		t.Fatalf("DropDatabase: %v", err)
	}

	exists, err = client.DatabaseExists(ctx, dbName)
	if err != nil {
		t.Fatalf("DatabaseExists after drop: %v", err)
	}
	if exists {
		t.Fatal("database should not exist after drop")
	}
}

func TestIntegration_DatabaseCollation(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	dbName := uniqueName("testdb_coll")
	collation := "Latin1_General_CI_AS"

	if err := client.CreateDatabase(ctx, dbName, &collation); err != nil {
		t.Fatalf("CreateDatabase with collation: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	got, err := client.GetDatabaseCollation(ctx, dbName)
	if err != nil {
		t.Fatalf("GetDatabaseCollation: %v", err)
	}
	if got != collation {
		t.Errorf("expected collation %q, got %q", collation, got)
	}
}

func TestIntegration_DatabaseOwner(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	dbName := uniqueName("testdb_own")
	loginName := uniqueName("testlogin_own")

	// Create a login to use as owner
	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	if err := client.CreateDatabase(ctx, dbName, nil); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	// Get initial owner (should be 'sa')
	owner, err := client.GetDatabaseOwner(ctx, dbName)
	if err != nil {
		t.Fatalf("GetDatabaseOwner: %v", err)
	}
	if owner != "sa" {
		t.Logf("initial owner: %q (expected sa)", owner)
	}

	// Change owner
	if err := client.SetDatabaseOwner(ctx, dbName, loginName); err != nil {
		t.Fatalf("SetDatabaseOwner: %v", err)
	}

	owner, err = client.GetDatabaseOwner(ctx, dbName)
	if err != nil {
		t.Fatalf("GetDatabaseOwner after change: %v", err)
	}
	if owner != loginName {
		t.Errorf("expected owner %q, got %q", loginName, owner)
	}
}

// --- Login integration tests ---

func TestIntegration_LoginLifecycle(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	loginName := uniqueName("testlogin")

	// Should not exist
	exists, err := client.LoginExists(ctx, loginName)
	if err != nil {
		t.Fatalf("LoginExists: %v", err)
	}
	if exists {
		t.Fatal("login should not exist yet")
	}

	// Create
	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	// Should exist
	exists, err = client.LoginExists(ctx, loginName)
	if err != nil {
		t.Fatalf("LoginExists after create: %v", err)
	}
	if !exists {
		t.Fatal("login should exist after creation")
	}

	// Update password
	if err := client.UpdateLoginPassword(ctx, loginName, "N3wP@ssw0rd!"); err != nil {
		t.Fatalf("UpdateLoginPassword: %v", err)
	}

	// Drop
	if err := client.DropLogin(ctx, loginName); err != nil {
		t.Fatalf("DropLogin: %v", err)
	}

	exists, err = client.LoginExists(ctx, loginName)
	if err != nil {
		t.Fatalf("LoginExists after drop: %v", err)
	}
	if exists {
		t.Fatal("login should not exist after drop")
	}
}

func TestIntegration_LoginDefaultDatabase(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	loginName := uniqueName("testlogin_db")
	dbName := uniqueName("testdb_def")

	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	if err := client.CreateDatabase(ctx, dbName, nil); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	// Default database should be 'master' initially
	defaultDB, err := client.GetLoginDefaultDatabase(ctx, loginName)
	if err != nil {
		t.Fatalf("GetLoginDefaultDatabase: %v", err)
	}
	if defaultDB != "master" {
		t.Logf("initial default database: %q", defaultDB)
	}

	// Set default database
	if err := client.SetLoginDefaultDatabase(ctx, loginName, dbName); err != nil {
		t.Fatalf("SetLoginDefaultDatabase: %v", err)
	}

	defaultDB, err = client.GetLoginDefaultDatabase(ctx, loginName)
	if err != nil {
		t.Fatalf("GetLoginDefaultDatabase after set: %v", err)
	}
	if defaultDB != dbName {
		t.Errorf("expected default database %q, got %q", dbName, defaultDB)
	}
}

func TestIntegration_LoginServerRoles(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	loginName := uniqueName("testlogin_roles")

	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	// Initially no custom roles
	roles, err := client.GetLoginServerRoles(ctx, loginName)
	if err != nil {
		t.Fatalf("GetLoginServerRoles: %v", err)
	}
	if containsRole(roles, "dbcreator") {
		t.Fatal("should not have dbcreator role initially")
	}

	// Add role
	if err := client.AddLoginToServerRole(ctx, loginName, "dbcreator"); err != nil {
		t.Fatalf("AddLoginToServerRole: %v", err)
	}

	roles, err = client.GetLoginServerRoles(ctx, loginName)
	if err != nil {
		t.Fatalf("GetLoginServerRoles after add: %v", err)
	}
	if !containsRole(roles, "dbcreator") {
		t.Errorf("expected dbcreator in roles, got %v", roles)
	}

	// Remove role
	if err := client.RemoveLoginFromServerRole(ctx, loginName, "dbcreator"); err != nil {
		t.Fatalf("RemoveLoginFromServerRole: %v", err)
	}

	roles, err = client.GetLoginServerRoles(ctx, loginName)
	if err != nil {
		t.Fatalf("GetLoginServerRoles after remove: %v", err)
	}
	if containsRole(roles, "dbcreator") {
		t.Errorf("dbcreator should have been removed, got %v", roles)
	}
}

// --- DatabaseUser integration tests ---

func TestIntegration_UserLifecycle(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	dbName := uniqueName("testdb_user")
	loginName := uniqueName("testlogin_user")
	userName := uniqueName("testuser")

	// Setup: create database and login
	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	if err := client.CreateDatabase(ctx, dbName, nil); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	// Should not exist
	exists, err := client.UserExists(ctx, dbName, userName)
	if err != nil {
		t.Fatalf("UserExists: %v", err)
	}
	if exists {
		t.Fatal("user should not exist yet")
	}

	// Create user
	if err := client.CreateUser(ctx, dbName, userName, loginName); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Should exist
	exists, err = client.UserExists(ctx, dbName, userName)
	if err != nil {
		t.Fatalf("UserExists after create: %v", err)
	}
	if !exists {
		t.Fatal("user should exist after creation")
	}

	// Drop user
	if err := client.DropUser(ctx, dbName, userName); err != nil {
		t.Fatalf("DropUser: %v", err)
	}

	exists, err = client.UserExists(ctx, dbName, userName)
	if err != nil {
		t.Fatalf("UserExists after drop: %v", err)
	}
	if exists {
		t.Fatal("user should not exist after drop")
	}
}

func TestIntegration_UserDatabaseRoles(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	dbName := uniqueName("testdb_roles")
	loginName := uniqueName("testlogin_roles2")
	userName := uniqueName("testuser_roles")

	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	if err := client.CreateDatabase(ctx, dbName, nil); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	if err := client.CreateUser(ctx, dbName, userName, loginName); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Add db_datareader role
	if err := client.AddUserToDatabaseRole(ctx, dbName, userName, "db_datareader"); err != nil {
		t.Fatalf("AddUserToDatabaseRole: %v", err)
	}

	roles, err := client.GetUserDatabaseRoles(ctx, dbName, userName)
	if err != nil {
		t.Fatalf("GetUserDatabaseRoles: %v", err)
	}
	if !containsRole(roles, "db_datareader") {
		t.Errorf("expected db_datareader in roles, got %v", roles)
	}

	// Add db_datawriter
	if err := client.AddUserToDatabaseRole(ctx, dbName, userName, "db_datawriter"); err != nil {
		t.Fatalf("AddUserToDatabaseRole: %v", err)
	}

	roles, err = client.GetUserDatabaseRoles(ctx, dbName, userName)
	if err != nil {
		t.Fatalf("GetUserDatabaseRoles: %v", err)
	}
	if len(roles) != 2 {
		t.Errorf("expected 2 roles, got %v", roles)
	}

	// Remove db_datareader
	if err := client.RemoveUserFromDatabaseRole(ctx, dbName, userName, "db_datareader"); err != nil {
		t.Fatalf("RemoveUserFromDatabaseRole: %v", err)
	}

	roles, err = client.GetUserDatabaseRoles(ctx, dbName, userName)
	if err != nil {
		t.Fatalf("GetUserDatabaseRoles after remove: %v", err)
	}
	if containsRole(roles, "db_datareader") {
		t.Errorf("db_datareader should have been removed, got %v", roles)
	}
	if !containsRole(roles, "db_datawriter") {
		t.Errorf("db_datawriter should still be present, got %v", roles)
	}
}

func TestIntegration_UserOwnsObjects(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	dbName := uniqueName("testdb_owns")
	loginName := uniqueName("testlogin_owns")
	userName := uniqueName("testuser_owns")

	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	if err := client.CreateDatabase(ctx, dbName, nil); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	if err := client.CreateUser(ctx, dbName, userName, loginName); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Initially user owns no objects
	owns, err := client.UserOwnsObjects(ctx, dbName, userName)
	if err != nil {
		t.Fatalf("UserOwnsObjects: %v", err)
	}
	if owns {
		t.Fatal("user should not own objects initially")
	}
}

func TestIntegration_LoginHasUsers(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()
	dbName := uniqueName("testdb_has")
	loginName := uniqueName("testlogin_has")
	userName := uniqueName("testuser_has")

	if err := client.CreateLogin(ctx, loginName, "P@ssw0rd123!"); err != nil {
		t.Fatalf("CreateLogin: %v", err)
	}
	t.Cleanup(func() { client.DropLogin(ctx, loginName) })

	// Initially no users
	has, err := client.LoginHasUsers(ctx, loginName)
	if err != nil {
		t.Fatalf("LoginHasUsers: %v", err)
	}
	if has {
		t.Fatal("login should not have users initially")
	}

	// Create database and user
	if err := client.CreateDatabase(ctx, dbName, nil); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(func() { client.DropDatabase(ctx, dbName) })

	if err := client.CreateUser(ctx, dbName, userName, loginName); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Now login should have users
	has, err = client.LoginHasUsers(ctx, loginName)
	if err != nil {
		t.Fatalf("LoginHasUsers after create: %v", err)
	}
	if !has {
		t.Error("login should have users after creating a database user")
	}

	// Drop user, login should have no users again
	if err := client.DropUser(ctx, dbName, userName); err != nil {
		t.Fatalf("DropUser: %v", err)
	}

	has, err = client.LoginHasUsers(ctx, loginName)
	if err != nil {
		t.Fatalf("LoginHasUsers after drop: %v", err)
	}
	if has {
		t.Error("login should not have users after dropping the database user")
	}
}

// --- Helpers ---

func containsRole(roles []string, target string) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

// ensureMSSQLContainer tries to start a SQL Server container if Docker is available.
// This is used in TestMain as a convenience — CI should start the container separately.
func ensureMSSQLContainer() error {
	// Check if MSSQL is already running
	connStr := buildConnectionString(testHost, testPort, testUser, testPassword, false)
	db, err := sql.Open("sqlserver", connStr)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if db.PingContext(ctx) == nil {
			db.Close()
			return nil // already running
		}
		db.Close()
	}

	// Try to start via Docker
	cmd := exec.Command("docker", "run", "-d", "--rm",
		"--name", "mssql-integration-test",
		"-e", "ACCEPT_EULA=Y",
		"-e", fmt.Sprintf("MSSQL_SA_PASSWORD=%s", testPassword),
		"-p", fmt.Sprintf("%d:1433", testPort),
		"mcr.microsoft.com/mssql/server:2022-latest")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start MSSQL container: %s: %w", output, err)
	}

	// Wait for SQL Server to be ready
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		db, err := sql.Open("sqlserver", connStr)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if db.PingContext(ctx) == nil {
			cancel()
			db.Close()
			return nil
		}
		cancel()
		db.Close()
	}
	return fmt.Errorf("MSSQL container did not become ready in time")
}
