//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

// =============================================================================
// Schema & Permission E2E Tests
// =============================================================================

func TestE2ESchemaLifecycle(t *testing.T) {
	// Prerequisite: create a database for schema tests
	dbKey := types.NamespacedName{Name: "schema-test-db", Namespace: testNamespace}
	schemaKey := types.NamespacedName{Name: "test-schema", Namespace: testNamespace}

	// Create database
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "schematest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	t.Run("CreateSchema", func(t *testing.T) {
		schema := &mssqlv1.Schema{
			ObjectMeta: metav1.ObjectMeta{Name: schemaKey.Name, Namespace: schemaKey.Namespace},
			Spec: mssqlv1.SchemaSpec{
				Server:         serverRef(),
				DatabaseName:   "schematest",
				SchemaName:     "app",
				Owner:          ptr("dbo"),
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, schema); err != nil {
			t.Fatalf("Failed to create Schema CR: %v", err)
		}

		waitForReady(t, schemaKey, &mssqlv1.Schema{})

		// Verify schema exists on SQL Server
		exists, err := sqlClient.SchemaExists(ctx, "schematest", "app")
		if err != nil {
			t.Fatalf("Failed to check schema existence: %v", err)
		}
		if !exists {
			t.Fatal("Schema 'app' does not exist in database 'schematest'")
		}
	})

	t.Run("UpdateSchemaOwner", func(t *testing.T) {
		var schema mssqlv1.Schema
		if err := k8sClient.Get(ctx, schemaKey, &schema); err != nil {
			t.Fatalf("Failed to get Schema: %v", err)
		}
		schema.Spec.Owner = ptr("dbo")
		if err := k8sClient.Update(ctx, &schema); err != nil {
			t.Fatalf("Failed to update Schema: %v", err)
		}
		waitForReady(t, schemaKey, &mssqlv1.Schema{})

		owner, err := sqlClient.GetSchemaOwner(ctx, "schematest", "app")
		if err != nil {
			t.Fatalf("Failed to get schema owner: %v", err)
		}
		if owner != "dbo" {
			t.Errorf("Expected schema owner 'dbo', got '%s'", owner)
		}
	})

	t.Run("DeleteSchema", func(t *testing.T) {
		var schema mssqlv1.Schema
		if err := k8sClient.Get(ctx, schemaKey, &schema); err != nil {
			t.Fatalf("Failed to get Schema: %v", err)
		}
		if err := k8sClient.Delete(ctx, &schema); err != nil {
			t.Fatalf("Failed to delete Schema CR: %v", err)
		}
		waitForDeletion(t, schemaKey, &mssqlv1.Schema{}, pollTimeout)

		// With Delete policy, schema should be dropped
		exists, err := sqlClient.SchemaExists(ctx, "schematest", "app")
		if err != nil {
			t.Fatalf("Failed to check schema existence: %v", err)
		}
		if exists {
			t.Fatal("Schema 'app' should have been dropped with DeletionPolicy=Delete")
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, db)
}

func TestE2EPermissionLifecycle(t *testing.T) {
	// Setup: database + login + user for permission tests
	dbKey := types.NamespacedName{Name: "perm-test-db", Namespace: testNamespace}
	loginKey := types.NamespacedName{Name: "perm-test-login", Namespace: testNamespace}
	userKey := types.NamespacedName{Name: "perm-test-user", Namespace: testNamespace}
	schemaKey := types.NamespacedName{Name: "perm-test-schema", Namespace: testNamespace}
	permKey := types.NamespacedName{Name: "perm-test-perms", Namespace: testNamespace}

	// Create database
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "permtest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// Create login
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "perm-login-password", Namespace: testNamespace},
		StringData: map[string]string{"password": "PermP@ss123!"},
	}
	if err := k8sClient.Create(ctx, pwSecret); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create password secret: %v", err)
	}

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: loginKey.Name, Namespace: loginKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "permlogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "perm-login-password"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Login CR: %v", err)
	}
	waitForReady(t, loginKey, &mssqlv1.Login{})

	// Create database user
	user := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "permtest",
			UserName:     "permuser",
			LoginRef:     mssqlv1.LoginReference{Name: "perm-test-login"},
		},
	}
	if err := k8sClient.Create(ctx, user); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create DatabaseUser CR: %v", err)
	}
	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Create a schema for permission targets
	schema := &mssqlv1.Schema{
		ObjectMeta: metav1.ObjectMeta{Name: schemaKey.Name, Namespace: schemaKey.Namespace},
		Spec: mssqlv1.SchemaSpec{
			Server:         serverRef(),
			DatabaseName:   "permtest",
			SchemaName:     "appdata",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, schema); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Schema CR: %v", err)
	}
	waitForReady(t, schemaKey, &mssqlv1.Schema{})

	t.Run("GrantPermissions", func(t *testing.T) {
		perm := &mssqlv1.Permission{
			ObjectMeta: metav1.ObjectMeta{Name: permKey.Name, Namespace: permKey.Namespace},
			Spec: mssqlv1.PermissionSpec{
				Server:       serverRef(),
				DatabaseName: "permtest",
				UserName:     "permuser",
				Grants: []mssqlv1.PermissionEntry{
					{Permission: "SELECT", On: "SCHEMA::appdata"},
					{Permission: "INSERT", On: "SCHEMA::appdata"},
				},
			},
		}
		if err := k8sClient.Create(ctx, perm); err != nil {
			t.Fatalf("Failed to create Permission CR: %v", err)
		}

		waitForReady(t, permKey, &mssqlv1.Permission{})

		// Verify permissions on SQL Server
		perms, err := sqlClient.GetPermissions(ctx, "permtest", "permuser")
		if err != nil {
			t.Fatalf("Failed to get permissions: %v", err)
		}
		foundSelect := false
		foundInsert := false
		for _, p := range perms {
			if p.Permission == "SELECT" && p.State == "GRANT" {
				foundSelect = true
			}
			if p.Permission == "INSERT" && p.State == "GRANT" {
				foundInsert = true
			}
		}
		if !foundSelect {
			t.Error("Expected SELECT GRANT on SCHEMA::appdata")
		}
		if !foundInsert {
			t.Error("Expected INSERT GRANT on SCHEMA::appdata")
		}
	})

	t.Run("AddDenyPermission", func(t *testing.T) {
		var perm mssqlv1.Permission
		if err := k8sClient.Get(ctx, permKey, &perm); err != nil {
			t.Fatalf("Failed to get Permission: %v", err)
		}
		perm.Spec.Denies = []mssqlv1.PermissionEntry{
			{Permission: "DELETE", On: "SCHEMA::appdata"},
		}
		if err := k8sClient.Update(ctx, &perm); err != nil {
			t.Fatalf("Failed to update Permission: %v", err)
		}

		// Wait for SQL state to converge (not just Ready=True which may be stale)
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			perms, err := sqlClient.GetPermissions(ctx, "permtest", "permuser")
			if err != nil {
				return false, nil
			}
			for _, p := range perms {
				if p.Permission == "DELETE" && p.State == "DENY" {
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			t.Fatal("DELETE DENY on SCHEMA::appdata did not converge")
		}
	})

	t.Run("DeletePermission_RevokesAll", func(t *testing.T) {
		var perm mssqlv1.Permission
		if err := k8sClient.Get(ctx, permKey, &perm); err != nil {
			t.Fatalf("Failed to get Permission: %v", err)
		}
		if err := k8sClient.Delete(ctx, &perm); err != nil {
			t.Fatalf("Failed to delete Permission CR: %v", err)
		}
		waitForDeletion(t, permKey, &mssqlv1.Permission{}, pollTimeout)

		// All grants and denies should be revoked
		perms, err := sqlClient.GetPermissions(ctx, "permtest", "permuser")
		if err != nil {
			t.Fatalf("Failed to get permissions: %v", err)
		}
		for _, p := range perms {
			if p.State == "GRANT" || p.State == "DENY" {
				t.Errorf("Expected all permissions revoked, but found %s %s", p.State, p.Permission)
			}
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, user)
	_ = k8sClient.Delete(ctx, schema)
	_ = k8sClient.Delete(ctx, login)
	_ = k8sClient.Delete(ctx, db)
}
