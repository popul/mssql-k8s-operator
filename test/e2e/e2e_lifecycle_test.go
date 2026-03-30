//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	appsv1 "k8s.io/api/apps/v1"

	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	internalsql "github.com/popul/mssql-k8s-operator/internal/sql"
)

// --- E2E Lifecycle Test ---

func TestE2EFullLifecycle(t *testing.T) {
	var (
		dbKey   = types.NamespacedName{Name: "test-db", Namespace: testNamespace}
		lgKey   = types.NamespacedName{Name: "test-login", Namespace: testNamespace}
		userKey = types.NamespacedName{Name: "test-dbuser", Namespace: testNamespace}
	)

	t.Run("CreateDatabase", func(t *testing.T) {
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dbKey.Name,
				Namespace: dbKey.Namespace,
			},
			Spec: mssqlv1.DatabaseSpec{
				Server:         serverRef(),
				DatabaseName:   "e2etest",
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, db); err != nil {
			t.Fatalf("Failed to create Database CR: %v", err)
		}

		waitForReady(t, dbKey, &mssqlv1.Database{})

		exists, err := sqlClient.DatabaseExists(ctx, "e2etest")
		if err != nil {
			t.Fatalf("Failed to check database existence: %v", err)
		}
		if !exists {
			t.Fatal("Database e2etest does not exist on SQL Server")
		}
	})

	t.Run("CreateLogin", func(t *testing.T) {
		// Create password secret
		pwSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-login-password", Namespace: testNamespace},
			StringData: map[string]string{"password": "LoginP@ss123!"},
		}
		if err := k8sClient.Create(ctx, pwSecret); err != nil && !errors.IsAlreadyExists(err) {
			t.Fatalf("Failed to create login password secret: %v", err)
		}

		login := &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{
				Name:      lgKey.Name,
				Namespace: lgKey.Namespace,
			},
			Spec: mssqlv1.LoginSpec{
				Server:         serverRef(),
				LoginName:      "e2elogin",
				PasswordSecret: mssqlv1.SecretReference{Name: "test-login-password"},
				ServerRoles:    []string{"dbcreator"},
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, login); err != nil {
			t.Fatalf("Failed to create Login CR: %v", err)
		}

		waitForReady(t, lgKey, &mssqlv1.Login{})

		exists, err := sqlClient.LoginExists(ctx, "e2elogin")
		if err != nil {
			t.Fatalf("Failed to check login existence: %v", err)
		}
		if !exists {
			t.Fatal("Login e2elogin does not exist on SQL Server")
		}

		roles, err := sqlClient.GetLoginServerRoles(ctx, "e2elogin")
		if err != nil {
			t.Fatalf("Failed to get login server roles: %v", err)
		}
		if !containsString(roles, "dbcreator") {
			t.Fatalf("Expected login to have role dbcreator, got: %v", roles)
		}
	})

	t.Run("CreateDatabaseUser", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userKey.Name,
				Namespace: userKey.Namespace,
			},
			Spec: mssqlv1.DatabaseUserSpec{
				Server:        serverRef(),
				DatabaseName:  "e2etest",
				UserName:      "e2euser",
				LoginRef:      mssqlv1.LoginReference{Name: "test-login"},
				DatabaseRoles: []string{"db_datareader", "db_datawriter"},
			},
		}
		if err := k8sClient.Create(ctx, dbUser); err != nil {
			t.Fatalf("Failed to create DatabaseUser CR: %v", err)
		}

		waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

		exists, err := sqlClient.UserExists(ctx, "e2etest", "e2euser")
		if err != nil {
			t.Fatalf("Failed to check user existence: %v", err)
		}
		if !exists {
			t.Fatal("User e2euser does not exist in e2etest")
		}

		roles, err := sqlClient.GetUserDatabaseRoles(ctx, "e2etest", "e2euser")
		if err != nil {
			t.Fatalf("Failed to get user database roles: %v", err)
		}
		assertContains(t, roles, "db_datareader")
		assertContains(t, roles, "db_datawriter")
	})

	t.Run("ModifyRoles_RemoveRole", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{}
		if err := k8sClient.Get(ctx, userKey, dbUser); err != nil {
			t.Fatalf("Failed to get DatabaseUser: %v", err)
		}

		dbUser.Spec.DatabaseRoles = []string{"db_datareader"}
		if err := k8sClient.Update(ctx, dbUser); err != nil {
			t.Fatalf("Failed to update DatabaseUser: %v", err)
		}

		// Wait for the roles to converge on SQL Server
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			roles, err := sqlClient.GetUserDatabaseRoles(ctx, "e2etest", "e2euser")
			if err != nil {
				return false, nil
			}
			return containsString(roles, "db_datareader") && !containsString(roles, "db_datawriter"), nil
		})
		if err != nil {
			t.Fatal("Roles did not converge after removing db_datawriter")
		}
	})

	t.Run("ModifyRoles_AddRoles", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{}
		if err := k8sClient.Get(ctx, userKey, dbUser); err != nil {
			t.Fatalf("Failed to get DatabaseUser: %v", err)
		}

		dbUser.Spec.DatabaseRoles = []string{"db_datareader", "db_datawriter", "db_ddladmin"}
		if err := k8sClient.Update(ctx, dbUser); err != nil {
			t.Fatalf("Failed to update DatabaseUser: %v", err)
		}

		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			roles, err := sqlClient.GetUserDatabaseRoles(ctx, "e2etest", "e2euser")
			if err != nil {
				return false, nil
			}
			return containsString(roles, "db_datareader") &&
				containsString(roles, "db_datawriter") &&
				containsString(roles, "db_ddladmin"), nil
		})
		if err != nil {
			t.Fatal("Roles did not converge after adding db_datawriter and db_ddladmin")
		}
	})

	t.Run("DeleteDatabaseUser", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{
			ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		}
		if err := k8sClient.Delete(ctx, dbUser); err != nil {
			t.Fatalf("Failed to delete DatabaseUser: %v", err)
		}

		waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)

		exists, err := sqlClient.UserExists(ctx, "e2etest", "e2euser")
		if err != nil {
			t.Fatalf("Failed to check user existence after deletion: %v", err)
		}
		if exists {
			t.Fatal("User e2euser still exists after CR deletion")
		}
	})

	t.Run("DeleteLogin", func(t *testing.T) {
		login := &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		}
		if err := k8sClient.Delete(ctx, login); err != nil {
			t.Fatalf("Failed to delete Login: %v", err)
		}

		waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)

		exists, err := sqlClient.LoginExists(ctx, "e2elogin")
		if err != nil {
			t.Fatalf("Failed to check login existence after deletion: %v", err)
		}
		if exists {
			t.Fatal("Login e2elogin still exists after CR deletion")
		}
	})

	t.Run("DeleteDatabase", func(t *testing.T) {
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		}
		if err := k8sClient.Delete(ctx, db); err != nil {
			t.Fatalf("Failed to delete Database: %v", err)
		}

		waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)

		exists, err := sqlClient.DatabaseExists(ctx, "e2etest")
		if err != nil {
			t.Fatalf("Failed to check database existence after deletion: %v", err)
		}
		if exists {
			t.Fatal("Database e2etest still exists after CR deletion")
		}
	})
}

func TestE2EIdempotence(t *testing.T) {
	key := types.NamespacedName{Name: "test-idempotent-db", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "e2eidempotent",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Record the status after initial reconciliation
	initial := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, initial); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}
	initialGeneration := initial.Status.ObservedGeneration
	initialCond := meta.FindStatusCondition(initial.Status.Conditions, mssqlv1.ConditionReady)

	// Wait for a few reconciliation cycles
	time.Sleep(15 * time.Second)

	// Verify nothing changed
	after := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, after); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}

	if after.Status.ObservedGeneration != initialGeneration {
		t.Errorf("ObservedGeneration changed: %d -> %d", initialGeneration, after.Status.ObservedGeneration)
	}

	afterCond := meta.FindStatusCondition(after.Status.Conditions, mssqlv1.ConditionReady)
	if afterCond == nil {
		t.Fatal("Ready condition disappeared")
	}
	if afterCond.Status != initialCond.Status {
		t.Errorf("Ready condition status changed: %s -> %s", initialCond.Status, afterCond.Status)
	}
	if !afterCond.LastTransitionTime.Equal(&initialCond.LastTransitionTime) {
		t.Errorf("LastTransitionTime changed unexpectedly")
	}
}

// --- Error cases ---

func TestE2ESecretNotFound(t *testing.T) {
	key := types.NamespacedName{Name: "test-secret-missing", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server: mssqlv1.ServerReference{
				Host: fmt.Sprintf("mssql.%s.svc.cluster.local", testNamespace),
				Port: ptr(int32(1433)),
				CredentialsSecret: mssqlv1.SecretReference{
					Name: "nonexistent-secret",
				},
			},
			DatabaseName:   "secretmissingdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
	}()

	waitForConditionWithReason(t, key, &mssqlv1.Database{}, mssqlv1.ConditionReady, metav1.ConditionFalse, mssqlv1.ReasonSecretNotFound, pollTimeout)
}

func TestE2ESQLServerUnreachable(t *testing.T) {
	// Create a credentials secret so the controller gets past that check
	unreachableSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "unreachable-creds", Namespace: testNamespace},
		StringData: map[string]string{"username": "sa", "password": "fake"},
	}
	if err := k8sClient.Create(ctx, unreachableSecret); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create secret: %v", err)
	}

	key := types.NamespacedName{Name: "test-unreachable", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server: mssqlv1.ServerReference{
				Host: "unreachable-host.invalid",
				Port: ptr(int32(1433)),
				CredentialsSecret: mssqlv1.SecretReference{
					Name: "unreachable-creds",
				},
			},
			DatabaseName:   "unreachabledb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyRetain),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
	}()

	// The controller returns err on connection failure (retry with backoff) without setting condition.
	// After multiple retries, the CR should still have no Ready=True condition.
	// Wait a bit and verify the CR is not Ready.
	time.Sleep(15 * time.Second)
	got := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, got); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, mssqlv1.ConditionReady)
	if cond != nil && cond.Status == metav1.ConditionTrue {
		t.Fatal("Database should NOT be Ready with unreachable SQL Server")
	}
}

// --- DeletionPolicy Retain ---

func TestE2EDeletionPolicyRetain(t *testing.T) {
	// Create a database with Retain policy
	key := types.NamespacedName{Name: "test-retain-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "retaintest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyRetain),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}

	waitForReady(t, key, &mssqlv1.Database{})

	// Delete the CR
	if err := k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
	}); err != nil {
		t.Fatalf("Failed to delete Database CR: %v", err)
	}

	waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)

	// Database should still exist on SQL Server
	exists, err := sqlClient.DatabaseExists(ctx, "retaintest")
	if err != nil {
		t.Fatalf("Failed to check database existence: %v", err)
	}
	if !exists {
		t.Fatal("Database retaintest should still exist with Retain policy, but it was dropped")
	}

	// Cleanup: drop manually
	_ = sqlClient.DropDatabase(ctx, "retaintest")
}

func TestE2EDeletionPolicyRetainLogin(t *testing.T) {
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "retain-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "RetainP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	key := types.NamespacedName{Name: "test-retain-login", Namespace: testNamespace}
	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "retainlogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "retain-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyRetain),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login CR: %v", err)
	}

	waitForReady(t, key, &mssqlv1.Login{})

	if err := k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
	}); err != nil {
		t.Fatalf("Failed to delete Login CR: %v", err)
	}

	waitForDeletion(t, key, &mssqlv1.Login{}, 60*time.Second)

	exists, err := sqlClient.LoginExists(ctx, "retainlogin")
	if err != nil {
		t.Fatalf("Failed to check login existence: %v", err)
	}
	if !exists {
		t.Fatal("Login retainlogin should still exist with Retain policy")
	}

	// Cleanup
	_ = sqlClient.DropLogin(ctx, "retainlogin")
}

// --- LoginInUse ---

func TestE2ELoginInUse(t *testing.T) {
	// Setup: create DB, Login, DatabaseUser
	dbKey := types.NamespacedName{Name: "test-inuse-db", Namespace: testNamespace}
	lgKey := types.NamespacedName{Name: "test-inuse-login", Namespace: testNamespace}
	userKey := types.NamespacedName{Name: "test-inuse-user", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "inusedb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "inuse-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "InUseP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "inuselogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "inuse-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login: %v", err)
	}
	waitForReady(t, lgKey, &mssqlv1.Login{})

	dbUser := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "inusedb",
			UserName:     "inuseuser",
			LoginRef:     mssqlv1.LoginReference{Name: "test-inuse-login"},
		},
	}
	if err := k8sClient.Create(ctx, dbUser); err != nil {
		t.Fatalf("Failed to create DatabaseUser: %v", err)
	}
	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Try to delete the Login while user exists
	if err := k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
	}); err != nil {
		t.Fatalf("Failed to delete Login: %v", err)
	}

	// Login should get stuck with LoginInUse
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		lg := &mssqlv1.Login{}
		if err := k8sClient.Get(ctx, lgKey, lg); err != nil {
			return false, nil
		}
		cond := meta.FindStatusCondition(lg.Status.Conditions, mssqlv1.ConditionReady)
		if cond == nil {
			return false, nil
		}
		return cond.Status == metav1.ConditionFalse && cond.Reason == mssqlv1.ReasonLoginInUse, nil
	})
	if err != nil {
		t.Fatal("Login should be stuck with LoginInUse condition")
	}

	// Login should still exist on SQL Server
	exists, err := sqlClient.LoginExists(ctx, "inuselogin")
	if err != nil {
		t.Fatalf("Failed to check login: %v", err)
	}
	if !exists {
		t.Fatal("Login should not have been dropped while in use")
	}

	// Cleanup: delete user first, then login can proceed
	_ = k8sClient.Delete(ctx, &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
	})
	waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)
	waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)

	// Cleanup DB
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
	})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)
}

// --- UserOwnsObjects ---

func TestE2EUserOwnsObjects(t *testing.T) {
	dbKey := types.NamespacedName{Name: "test-owns-db", Namespace: testNamespace}
	lgKey := types.NamespacedName{Name: "test-owns-login", Namespace: testNamespace}
	userKey := types.NamespacedName{Name: "test-owns-user", Namespace: testNamespace}

	// Create DB
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "ownsdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, db)
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// Create Login
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "owns-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "OwnsP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "ownslogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "owns-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, login)
	waitForReady(t, lgKey, &mssqlv1.Login{})

	// Create DatabaseUser
	dbUser := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "ownsdb",
			UserName:     "ownsuser",
			LoginRef:     mssqlv1.LoginReference{Name: "test-owns-login"},
		},
	}
	_ = k8sClient.Create(ctx, dbUser)
	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Create a schema owned by this user via raw SQL
	execRawSQL(t, "ownsdb", "CREATE SCHEMA [ownedschema] AUTHORIZATION [ownsuser]")

	// Try to delete the user
	_ = k8sClient.Delete(ctx, &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
	})

	// Should be stuck with UserOwnsObjects
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		u := &mssqlv1.DatabaseUser{}
		if err := k8sClient.Get(ctx, userKey, u); err != nil {
			return false, nil
		}
		cond := meta.FindStatusCondition(u.Status.Conditions, mssqlv1.ConditionReady)
		if cond == nil {
			return false, nil
		}
		return cond.Status == metav1.ConditionFalse && cond.Reason == mssqlv1.ReasonUserOwnsObjects, nil
	})
	if err != nil {
		t.Fatal("DatabaseUser should be stuck with UserOwnsObjects condition")
	}

	// Cleanup: transfer schema ownership, then user can be deleted
	execRawSQL(t, "ownsdb", "ALTER AUTHORIZATION ON SCHEMA::[ownedschema] TO [dbo]")
	execRawSQL(t, "ownsdb", "DROP SCHEMA [ownedschema]")

	waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
	})
	waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
	})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)
}

// --- LoginRefNotFound + convergence ---

func TestE2ELoginRefNotFound(t *testing.T) {
	// Create DB first
	dbKey := types.NamespacedName{Name: "test-loginref-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "loginrefdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, db)
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// Create DatabaseUser referencing a Login that doesn't exist yet
	userKey := types.NamespacedName{Name: "test-loginref-user", Namespace: testNamespace}
	dbUser := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "loginrefdb",
			UserName:     "loginrefuser",
			LoginRef:     mssqlv1.LoginReference{Name: "test-loginref-login"},
		},
	}
	if err := k8sClient.Create(ctx, dbUser); err != nil {
		t.Fatalf("Failed to create DatabaseUser: %v", err)
	}

	// Should get LoginRefNotFound
	waitForConditionWithReason(t, userKey, &mssqlv1.DatabaseUser{}, mssqlv1.ConditionReady, metav1.ConditionFalse, mssqlv1.ReasonLoginRefNotFound, pollTimeout)

	// Now create the Login
	lgKey := types.NamespacedName{Name: "test-loginref-login", Namespace: testNamespace}
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "loginref-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "LoginRefP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "loginreflogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "loginref-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, login)
	waitForReady(t, lgKey, &mssqlv1.Login{})

	// The DatabaseUser controller doesn't requeue on LoginRefNotFound (permanent error by design).
	// We need to bump the generation to trigger reconciliation.
	triggerReconciliation(t, userKey, &mssqlv1.DatabaseUser{})

	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Verify on SQL Server
	exists, err := sqlClient.UserExists(ctx, "loginrefdb", "loginrefuser")
	if err != nil {
		t.Fatalf("Failed to check user: %v", err)
	}
	if !exists {
		t.Fatal("User loginrefuser should exist after Login was created")
	}

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
	})
	waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)
	_ = k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
	})
	waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
	})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)
}

// --- Collation immutable ---

func TestE2ECollationImmutable(t *testing.T) {
	key := types.NamespacedName{Name: "test-collation-db", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "collationdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Now set a different collation on the existing DB
	got := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, got); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}
	got.Spec.Collation = ptr("Latin1_General_BIN")
	if err := k8sClient.Update(ctx, got); err != nil {
		t.Fatalf("Failed to update Database: %v", err)
	}

	waitForConditionWithReason(t, key, &mssqlv1.Database{}, mssqlv1.ConditionReady, metav1.ConditionFalse, mssqlv1.ReasonCollationChangeNotSupported, pollTimeout)
}

// --- Password rotation ---

func TestE2EPasswordRotation(t *testing.T) {
	lgKey := types.NamespacedName{Name: "test-pwrotate-login", Namespace: testNamespace}

	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pwrotate-secret", Namespace: testNamespace},
		StringData: map[string]string{"password": "OldP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "pwrotatelogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "pwrotate-secret"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		})
		waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)
	}()

	waitForReady(t, lgKey, &mssqlv1.Login{})

	// Verify old password works
	newPassword := "NewP@ss456!"
	verifyLoginPassword(t, "pwrotatelogin", "OldP@ss123!")

	// Update the secret with a new password
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Name: "pwrotate-secret", Namespace: testNamespace}
	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		t.Fatalf("Failed to get password secret: %v", err)
	}
	secret.Data["password"] = []byte(newPassword)
	if err := k8sClient.Update(ctx, secret); err != nil {
		t.Fatalf("Failed to update password secret: %v", err)
	}

	// The controller detects password changes via PasswordSecretResourceVersion on periodic requeue (~30s).
	// Trigger reconciliation to speed things up.
	triggerReconciliation(t, lgKey, &mssqlv1.Login{})

	// Wait until the new password works
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		factory := internalsql.NewClientFactory()
		c, err := factory("localhost", 1433, "pwrotatelogin", newPassword, false)
		if err != nil {
			return false, nil
		}
		defer c.Close()
		return c.Ping(ctx) == nil, nil
	})
	if err != nil {
		t.Fatal("New password did not work after rotation")
	}
}

// --- Adoption ---

func TestE2EAdoption(t *testing.T) {
	// Create a database directly on SQL Server
	if err := sqlClient.CreateDatabase(ctx, "adopteddb", nil); err != nil {
		t.Fatalf("Failed to create database directly: %v", err)
	}

	key := types.NamespacedName{Name: "test-adopt-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "adopteddb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	// Should adopt the existing database and become Ready
	waitForReady(t, key, &mssqlv1.Database{})

	// Verify database still exists
	exists, err := sqlClient.DatabaseExists(ctx, "adopteddb")
	if err != nil {
		t.Fatalf("Failed to check database: %v", err)
	}
	if !exists {
		t.Fatal("Adopted database should still exist")
	}
}

func TestE2EAdoptionLogin(t *testing.T) {
	// Create a login directly on SQL Server
	if err := sqlClient.CreateLogin(ctx, "adoptedlogin", "AdoptP@ss123!"); err != nil {
		t.Fatalf("Failed to create login directly: %v", err)
	}

	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "adopt-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "AdoptP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	key := types.NamespacedName{Name: "test-adopt-login", Namespace: testNamespace}
	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "adoptedlogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "adopt-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Login{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Login{})
}

// --- Drift detection ---

func TestE2EDriftDetection(t *testing.T) {
	key := types.NamespacedName{Name: "test-drift-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "driftdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Drop the database manually (simulate drift)
	if err := sqlClient.DropDatabase(ctx, "driftdb"); err != nil {
		t.Fatalf("Failed to drop database: %v", err)
	}

	exists, _ := sqlClient.DatabaseExists(ctx, "driftdb")
	if exists {
		t.Fatal("Database should have been dropped")
	}

	// Trigger reconciliation (the periodic requeue should handle it, but let's speed it up)
	triggerReconciliation(t, key, &mssqlv1.Database{})

	// Wait for the database to be recreated
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		return sqlClient.DatabaseExists(ctx, "driftdb")
	})
	if err != nil {
		t.Fatal("Database was not recreated after drift")
	}
}

// --- SQL Server downtime ---

func TestE2ESQLServerDowntime(t *testing.T) {
	key := types.NamespacedName{Name: "test-downtime-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "downtimedb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 120*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Scale SQL Server to 0
	scaleSQLServer(t, 0)
	time.Sleep(10 * time.Second)

	// Scale back to 1
	scaleSQLServer(t, 1)

	// Wait for SQL Server to be ready again
	if err := waitForSQLServerReady(); err != nil {
		t.Fatalf("SQL Server did not come back: %v", err)
	}

	// Restart port-forward (old one died when pod was killed)
	if portFwdCmd != nil && portFwdCmd.Process != nil {
		_ = portFwdCmd.Process.Kill()
		_ = portFwdCmd.Wait()
	}
	if err := startPortForward(); err != nil {
		t.Fatalf("Failed to restart port-forward: %v", err)
	}

	// Reconnect SQL client (old connection is broken)
	if err := reconnectSQLClient(); err != nil {
		t.Fatalf("Failed to reconnect SQL client: %v", err)
	}

	// The operator should eventually reconcile and the CR should be Ready
	// Trigger reconciliation to speed things up
	triggerReconciliation(t, key, &mssqlv1.Database{})
	waitForReady(t, key, &mssqlv1.Database{})
}

// --- Operator restart ---

func TestE2EOperatorRestart(t *testing.T) {
	// Ensure SQL client is available (previous test may have disrupted it)
	if sqlClient == nil {
		if err := reconnectSQLClient(); err != nil {
			t.Fatalf("SQL client unavailable: %v", err)
		}
	}

	key := types.NamespacedName{Name: "test-restart-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "restartdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Kill the operator pod
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "pods",
		"-l", "app.kubernetes.io/name=mssql-operator",
		"-n", "mssql-operator-system",
		"--wait=false",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to delete operator pod: %v: %s", err, out)
	}

	// Wait for the pod to come back
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		dep := &appsv1.Deployment{}
		depKey := types.NamespacedName{Name: "mssql-operator", Namespace: "mssql-operator-system"}
		if err := k8sClient.Get(ctx, depKey, dep); err != nil {
			return false, nil
		}
		return dep.Status.ReadyReplicas >= 1, nil
	})
	if err != nil {
		t.Fatal("Operator did not come back after restart")
	}

	// Database should still be Ready after operator restart
	waitForReady(t, key, &mssqlv1.Database{})

	// Verify on SQL Server
	exists, err := sqlClient.DatabaseExists(ctx, "restartdb")
	if err != nil {
		t.Fatalf("Failed to check database: %v", err)
	}
	if !exists {
		t.Fatal("Database should still exist after operator restart")
	}
}
