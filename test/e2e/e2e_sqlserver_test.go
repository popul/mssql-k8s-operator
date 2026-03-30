//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"os/exec"

	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

// =============================================================================
// SQLServer CRD E2E Tests
// =============================================================================

func TestE2ESQLServerLifecycle(t *testing.T) {
	srvKey := types.NamespacedName{Name: "e2e-sqlserver", Namespace: testNamespace}

	t.Run("CreateSQLServer", func(t *testing.T) {
		srv := &mssqlv1.SQLServer{
			ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace},
			Spec: mssqlv1.SQLServerSpec{
				Host: fmt.Sprintf("mssql.%s.svc.cluster.local", testNamespace),
				Port: ptr(int32(1433)),
				CredentialsSecret: &mssqlv1.CrossNamespaceSecretReference{
					Name:      "mssql-sa-credentials",
					Namespace: ptr(testNamespace),
				},
				TLS:        ptr(false),
				AuthMethod: mssqlv1.AuthSqlLogin,
			},
		}
		if err := k8sClient.Create(ctx, srv); err != nil {
			t.Fatalf("Failed to create SQLServer CR: %v", err)
		}

		waitForReady(t, srvKey, &mssqlv1.SQLServer{})

		// Verify status fields
		var updated mssqlv1.SQLServer
		if err := k8sClient.Get(ctx, srvKey, &updated); err != nil {
			t.Fatalf("Failed to get SQLServer: %v", err)
		}
		if updated.Status.ServerVersion == "" {
			t.Error("Expected serverVersion to be set")
		}
		if updated.Status.Edition == "" {
			t.Error("Expected edition to be set")
		}
		if updated.Status.LastConnectedTime == nil {
			t.Error("Expected lastConnectedTime to be set")
		}
		t.Logf("SQLServer version=%s edition=%s", updated.Status.ServerVersion, updated.Status.Edition)
	})

	t.Run("DatabaseWithSQLServerRef", func(t *testing.T) {
		dbKey := types.NamespacedName{Name: "e2e-sqlserverref-db", Namespace: testNamespace}
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
			Spec: mssqlv1.DatabaseSpec{
				Server: mssqlv1.ServerReference{
					SQLServerRef: ptr(srvKey.Name),
					// credentialsSecret.name is required by CRD validation (MinLength=1)
					// even when sqlServerRef is set. Use a placeholder that will be
					// overridden by the controller's resolveServerReference.
					CredentialsSecret: mssqlv1.SecretReference{Name: "unused-placeholder"},
				},
				DatabaseName:   "sqlserverreftest",
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, db); err != nil {
			t.Fatalf("Failed to create Database with sqlServerRef: %v", err)
		}
		waitForReady(t, dbKey, &mssqlv1.Database{})

		// Verify the database actually exists on SQL Server
		exists, err := sqlClient.DatabaseExists(ctx, "sqlserverreftest")
		if err != nil {
			t.Fatalf("Failed to check database existence: %v", err)
		}
		if !exists {
			t.Fatal("Database 'sqlserverreftest' should exist on SQL Server")
		}

		// Cleanup
		_ = k8sClient.Delete(ctx, db)
		waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)
	})

	t.Run("Idempotent", func(t *testing.T) {
		// Re-fetch to ensure status is still Ready after multiple reconcile loops
		var srv mssqlv1.SQLServer
		if err := k8sClient.Get(ctx, srvKey, &srv); err != nil {
			t.Fatalf("Failed to get SQLServer: %v", err)
		}
		cond := findCondition(&srv, mssqlv1.ConditionReady)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Error("Expected SQLServer to remain Ready after multiple reconcile loops")
		}
	})

	t.Run("Cleanup", func(t *testing.T) {
		_ = k8sClient.Delete(ctx, &mssqlv1.SQLServer{ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace}})
		waitForDeletion(t, srvKey, &mssqlv1.SQLServer{}, pollTimeout)
	})
}

// =============================================================================
// Database Configuration E2E Tests (Recovery Model, Compat Level, Options)
// =============================================================================

func TestE2EDatabaseConfiguration(t *testing.T) {
	dbKey := types.NamespacedName{Name: "e2e-dbconfig", Namespace: testNamespace}

	// Create database with extended configuration
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "configtest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			RecoveryModel:  ptr(mssqlv1.RecoveryModelFull),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	t.Run("RecoveryModel_Full", func(t *testing.T) {
		model, err := sqlClient.GetDatabaseRecoveryModel(ctx, "configtest")
		if err != nil {
			t.Fatalf("Failed to get recovery model: %v", err)
		}
		if model != "FULL" {
			t.Errorf("Expected recovery model FULL, got %s", model)
		}
	})

	t.Run("ChangeRecoveryModel_ToSimple", func(t *testing.T) {
		var current mssqlv1.Database
		if err := k8sClient.Get(ctx, dbKey, &current); err != nil {
			t.Fatalf("Failed to get Database: %v", err)
		}
		current.Spec.RecoveryModel = ptr(mssqlv1.RecoveryModelSimple)
		if err := k8sClient.Update(ctx, &current); err != nil {
			t.Fatalf("Failed to update Database: %v", err)
		}

		// Wait for the SQL state to converge (not just Ready=True which may be stale)
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			model, err := sqlClient.GetDatabaseRecoveryModel(ctx, "configtest")
			if err != nil {
				return false, nil
			}
			return model == "SIMPLE", nil
		})
		if err != nil {
			t.Fatal("Recovery model did not converge to SIMPLE")
		}
	})

	t.Run("CompatibilityLevel", func(t *testing.T) {
		var current mssqlv1.Database
		if err := k8sClient.Get(ctx, dbKey, &current); err != nil {
			t.Fatalf("Failed to get Database: %v", err)
		}
		current.Spec.CompatibilityLevel = ptr(int32(150))
		if err := k8sClient.Update(ctx, &current); err != nil {
			t.Fatalf("Failed to update Database: %v", err)
		}

		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			level, err := sqlClient.GetDatabaseCompatibilityLevel(ctx, "configtest")
			if err != nil {
				return false, nil
			}
			return level == 150, nil
		})
		if err != nil {
			t.Fatal("Compatibility level did not converge to 150")
		}
	})

	t.Run("DatabaseOptions", func(t *testing.T) {
		var current mssqlv1.Database
		if err := k8sClient.Get(ctx, dbKey, &current); err != nil {
			t.Fatalf("Failed to get Database: %v", err)
		}
		current.Spec.Options = []mssqlv1.DatabaseOption{
			{Name: "AUTO_SHRINK", Value: true},
		}
		if err := k8sClient.Update(ctx, &current); err != nil {
			t.Fatalf("Failed to update Database: %v", err)
		}

		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			val, err := sqlClient.GetDatabaseOption(ctx, "configtest", "IsAutoShrink")
			if err != nil {
				return false, nil
			}
			return val, nil
		})
		if err != nil {
			t.Fatal("AUTO_SHRINK did not converge to ON")
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace}})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)
}

// =============================================================================
// Managed SQLServer E2E Tests (operator-managed StatefulSet/Services/Certs/AG)
// =============================================================================

// TestE2EManagedSQLServer_Standalone tests a managed SQLServer CR with 1 replica.
// The operator should create StatefulSet, Services, PVCs, and probe SQL connectivity.
func TestE2EManagedSQLServer_Standalone(t *testing.T) {
	srvKey := types.NamespacedName{Name: "managed-standalone", Namespace: testNamespace}

	t.Run("CreateManagedSQLServer", func(t *testing.T) {
		srv := &mssqlv1.SQLServer{
			ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace},
			Spec: mssqlv1.SQLServerSpec{
				CredentialsSecret: &mssqlv1.CrossNamespaceSecretReference{
					Name: "mssql-sa-credentials",
				},
				Instance: &mssqlv1.InstanceSpec{
					AcceptEULA:       true,
					SAPasswordSecret: mssqlv1.SecretReference{Name: "mssql-sa-password"},
					Replicas:         ptr(int32(1)),
					StorageSize:      ptr("1Gi"),
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("512Mi"),
							corev1.ResourceCPU:    resource.MustParse("250m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
		}
		if err := k8sClient.Create(ctx, srv); err != nil {
			t.Fatalf("Failed to create managed SQLServer CR: %v", err)
		}
	})

	t.Run("StatefulSetCreated", func(t *testing.T) {
		stsKey := types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var sts appsv1.StatefulSet
			if err := k8sClient.Get(ctx, stsKey, &sts); err != nil {
				return false, nil
			}
			return sts.Spec.Replicas != nil && *sts.Spec.Replicas == 1, nil
		})
		if err != nil {
			t.Fatalf("StatefulSet was not created: %v", err)
		}
		t.Log("StatefulSet created with 1 replica")
	})

	t.Run("HeadlessServiceCreated", func(t *testing.T) {
		var svc corev1.Service
		headlessKey := types.NamespacedName{Name: srvKey.Name + "-headless", Namespace: srvKey.Namespace}
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, headlessKey, &svc); err != nil {
				return false, nil
			}
			return svc.Spec.ClusterIP == "None", nil
		})
		if err != nil {
			t.Fatalf("Headless Service was not created: %v", err)
		}
		t.Log("Headless Service created")
	})

	t.Run("ClientServiceCreated", func(t *testing.T) {
		var svc corev1.Service
		clientKey := types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, clientKey, &svc); err != nil {
				return false, nil
			}
			return svc.Spec.Type == corev1.ServiceTypeClusterIP, nil
		})
		if err != nil {
			t.Fatalf("Client Service was not created: %v", err)
		}
		// Verify port
		found := false
		for _, p := range svc.Spec.Ports {
			if p.Port == 1433 {
				found = true
			}
		}
		if !found {
			t.Error("Expected client Service to expose port 1433")
		}
		t.Log("Client Service created with port 1433")
	})

	t.Run("SQLServerBecomesReady", func(t *testing.T) {
		// Wait for StatefulSet pods to be ready first
		stsKey := types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
			var sts appsv1.StatefulSet
			if err := k8sClient.Get(ctx, stsKey, &sts); err != nil {
				return false, nil
			}
			return sts.Status.ReadyReplicas >= 1, nil
		})
		if err != nil {
			t.Fatalf("StatefulSet pods did not become ready: %v", err)
		}

		// Wait for SQLServer CR to become Ready
		waitForReady(t, srvKey, &mssqlv1.SQLServer{})

		var srv mssqlv1.SQLServer
		if err := k8sClient.Get(ctx, srvKey, &srv); err != nil {
			t.Fatalf("Failed to get SQLServer: %v", err)
		}

		// Verify status fields
		if srv.Status.ServerVersion == "" {
			t.Error("Expected serverVersion to be set")
		}
		if srv.Status.Edition == "" {
			t.Error("Expected edition to be set")
		}
		expectedHost := fmt.Sprintf("%s.%s.svc.cluster.local", srvKey.Name, srvKey.Namespace)
		if srv.Status.Host != expectedHost {
			t.Errorf("Expected host=%s, got %s", expectedHost, srv.Status.Host)
		}
		if srv.Status.ReadyReplicas == nil || *srv.Status.ReadyReplicas != 1 {
			t.Errorf("Expected readyReplicas=1, got %v", srv.Status.ReadyReplicas)
		}
		t.Logf("Managed standalone SQLServer ready: version=%s edition=%s host=%s",
			srv.Status.ServerVersion, srv.Status.Edition, srv.Status.Host)
	})

	t.Run("DatabaseViaSQLServerRef", func(t *testing.T) {
		// Create a Database CR referencing the managed SQLServer
		dbKey := types.NamespacedName{Name: "managed-db-test", Namespace: testNamespace}
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
			Spec: mssqlv1.DatabaseSpec{
				Server: mssqlv1.ServerReference{
					SQLServerRef:      ptr(srvKey.Name),
					CredentialsSecret: mssqlv1.SecretReference{Name: "unused-placeholder"},
				},
				DatabaseName:   "manageddbtest",
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, db); err != nil {
			t.Fatalf("Failed to create Database: %v", err)
		}
		waitForReady(t, dbKey, &mssqlv1.Database{})
		t.Log("Database created on managed SQLServer via sqlServerRef")

		// Cleanup
		_ = k8sClient.Delete(ctx, db)
		waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)
	})

	t.Run("Idempotent", func(t *testing.T) {
		// Verify the CR stays Ready after multiple reconcile loops
		time.Sleep(5 * time.Second) // Let a few reconcile cycles run
		var srv mssqlv1.SQLServer
		if err := k8sClient.Get(ctx, srvKey, &srv); err != nil {
			t.Fatalf("Failed to get SQLServer: %v", err)
		}
		cond := findCondition(&srv, mssqlv1.ConditionReady)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Error("Expected SQLServer to remain Ready")
		}
	})

	t.Run("OwnerReferencesOnResources", func(t *testing.T) {
		// Verify StatefulSet, headless Service, and client Service have owner references
		for _, name := range []string{srvKey.Name, srvKey.Name + "-headless"} {
			var svc corev1.Service
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: srvKey.Namespace}, &svc); err != nil {
				t.Fatalf("Failed to get Service %s: %v", name, err)
			}
			hasOwner := false
			for _, ref := range svc.OwnerReferences {
				if ref.Kind == "SQLServer" && ref.Name == srvKey.Name {
					hasOwner = true
				}
			}
			if !hasOwner {
				t.Errorf("Service %s missing owner reference to SQLServer", name)
			}
		}
		var sts appsv1.StatefulSet
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}, &sts); err != nil {
			t.Fatalf("Failed to get StatefulSet: %v", err)
		}
		hasOwner := false
		for _, ref := range sts.OwnerReferences {
			if ref.Kind == "SQLServer" && ref.Name == srvKey.Name {
				hasOwner = true
			}
		}
		if !hasOwner {
			t.Error("StatefulSet missing owner reference to SQLServer")
		}
	})

	t.Run("Cleanup", func(t *testing.T) {
		_ = k8sClient.Delete(ctx, &mssqlv1.SQLServer{
			ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace},
		})
		waitForDeletion(t, srvKey, &mssqlv1.SQLServer{}, pollTimeout)

		// Verify cascade deletion of StatefulSet
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}, &sts)
			return errors.IsNotFound(err), nil
		})
		if err != nil {
			t.Error("StatefulSet was not garbage collected after SQLServer deletion")
		}
		t.Log("Managed SQLServer and child resources cleaned up")
	})
}

// TestE2EManagedSQLServer_Cluster tests a managed SQLServer CR with 2 replicas,
// HADR enabled, certificate provisioning, AG creation, and auto-failover.
func TestE2EManagedSQLServer_Cluster(t *testing.T) {
	srvKey := types.NamespacedName{Name: "managed-cluster", Namespace: testNamespace}

	t.Run("CreateManagedCluster", func(t *testing.T) {
		srv := &mssqlv1.SQLServer{
			ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace},
			Spec: mssqlv1.SQLServerSpec{
				CredentialsSecret: &mssqlv1.CrossNamespaceSecretReference{
					Name: "mssql-sa-credentials",
				},
				Instance: &mssqlv1.InstanceSpec{
					AcceptEULA:       true,
					Image:            ptr(sqlImage),
					Edition:          ptr("Developer"),
					SAPasswordSecret: mssqlv1.SecretReference{Name: "mssql-sa-password"},
					Replicas:         ptr(int32(2)),
					StorageSize:      ptr("1Gi"),
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("512Mi"),
							corev1.ResourceCPU:    resource.MustParse("250m"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
					Certificates: &mssqlv1.CertificateSpec{
						Mode: ptr(mssqlv1.CertificateModeSelfSigned),
					},
					AvailabilityGroup: &mssqlv1.ManagedAGSpec{
						AGName:              ptr("managedclusterag"),
						AvailabilityMode:    ptr("SynchronousCommit"),
						AutoFailover:        ptr(true),
						HealthCheckInterval: ptr("5s"),
						FailoverCooldown:    ptr("30s"),
					},
				},
			},
		}
		if err := k8sClient.Create(ctx, srv); err != nil {
			t.Fatalf("Failed to create managed cluster SQLServer CR: %v", err)
		}
	})

	t.Run("StatefulSetWith2Replicas", func(t *testing.T) {
		stsKey := types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var sts appsv1.StatefulSet
			if err := k8sClient.Get(ctx, stsKey, &sts); err != nil {
				return false, nil
			}
			return sts.Spec.Replicas != nil && *sts.Spec.Replicas == 2, nil
		})
		if err != nil {
			t.Fatalf("StatefulSet with 2 replicas was not created: %v", err)
		}

		// Verify HADR port is exposed
		var sts appsv1.StatefulSet
		if err := k8sClient.Get(ctx, stsKey, &sts); err != nil {
			t.Fatalf("Failed to get StatefulSet: %v", err)
		}
		hasHADR := false
		for _, p := range sts.Spec.Template.Spec.Containers[0].Ports {
			if p.ContainerPort == 5022 && p.Name == "hadr" {
				hasHADR = true
			}
		}
		if !hasHADR {
			t.Error("Expected StatefulSet to expose HADR port 5022")
		}

		// Verify MSSQL_ENABLE_HADR env var is set
		hasHADREnv := false
		for _, e := range sts.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "MSSQL_ENABLE_HADR" && e.Value == "1" {
				hasHADREnv = true
			}
		}
		if !hasHADREnv {
			t.Error("Expected MSSQL_ENABLE_HADR=1 env var on StatefulSet")
		}
		t.Log("StatefulSet created with 2 replicas, HADR port and env configured")
	})

	t.Run("HeadlessServiceWithHADRPort", func(t *testing.T) {
		headlessKey := types.NamespacedName{Name: srvKey.Name + "-headless", Namespace: srvKey.Namespace}
		var svc corev1.Service
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, headlessKey, &svc); err != nil {
				return false, nil
			}
			return svc.Spec.ClusterIP == "None", nil
		})
		if err != nil {
			t.Fatalf("Headless Service was not created: %v", err)
		}

		// Verify HADR port
		hasHADR := false
		for _, p := range svc.Spec.Ports {
			if p.Port == 5022 && p.Name == "hadr" {
				hasHADR = true
			}
		}
		if !hasHADR {
			t.Error("Expected headless Service to expose HADR port 5022")
		}
		t.Log("Headless Service with HADR port created")
	})

	t.Run("CertificateSecretsCreated", func(t *testing.T) {
		// Wait for certificate secrets: CA + 2 per-replica certs
		caKey := types.NamespacedName{Name: srvKey.Name + "-ca", Namespace: srvKey.Namespace}
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var s corev1.Secret
			return k8sClient.Get(ctx, caKey, &s) == nil, nil
		})
		if err != nil {
			t.Fatalf("CA certificate secret was not created: %v", err)
		}

		for i := 0; i < 2; i++ {
			certKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-cert-%d", srvKey.Name, i),
				Namespace: srvKey.Namespace,
			}
			err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
				var s corev1.Secret
				if err := k8sClient.Get(ctx, certKey, &s); err != nil {
					return false, nil
				}
				// Verify cert and key are present
				return len(s.Data["tls.crt"]) > 0 && len(s.Data["tls.key"]) > 0, nil
			})
			if err != nil {
				t.Fatalf("Certificate secret for replica %d was not created: %v", i, err)
			}
		}

		// Verify CertificatesReady in status
		var srv mssqlv1.SQLServer
		err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, srvKey, &srv); err != nil {
				return false, nil
			}
			return srv.Status.CertificatesReady != nil && *srv.Status.CertificatesReady, nil
		})
		if err != nil {
			t.Fatalf("CertificatesReady was not set to true: %v", err)
		}
		t.Log("CA and per-replica certificate secrets created, CertificatesReady=true")
	})

	t.Run("BothReplicasReady", func(t *testing.T) {
		// Wait for both StatefulSet replicas to become ready
		stsKey := types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 4*time.Minute, true, func(ctx context.Context) (bool, error) {
			var sts appsv1.StatefulSet
			if err := k8sClient.Get(ctx, stsKey, &sts); err != nil {
				return false, nil
			}
			return sts.Status.ReadyReplicas >= 2, nil
		})
		if err != nil {
			t.Fatalf("StatefulSet pods did not all become ready: %v", err)
		}

		// Wait for SQL to accept connections on both pods
		for _, pod := range []string{srvKey.Name + "-0", srvKey.Name + "-1"} {
			t.Logf("Waiting for %s to accept SQL connections...", pod)
			err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
				cmd := exec.CommandContext(ctx, "kubectl", "exec", pod, "-n", testNamespace,
					"--", "/opt/mssql-tools18/bin/sqlcmd",
					"-S", "localhost", "-U", "sa", "-P", saPassword,
					"-Q", "SELECT 1", "-C", "-No")
				return cmd.Run() == nil, nil
			})
			if err != nil {
				t.Fatalf("Pod %s did not accept SQL connections: %v", pod, err)
			}
		}
		t.Log("Both replicas ready and accepting SQL connections")

		// Set up HADR certificates exchange between managed pods
		// (the operator creates local certs but peer exchange needs file copies)
		setupAGCertificatesForPods(t, []string{srvKey.Name + "-0", srvKey.Name + "-1"})
	})

	t.Run("SQLServerBecomesReady", func(t *testing.T) {
		waitForReady(t, srvKey, &mssqlv1.SQLServer{})

		// Wait for primaryReplica to be set (AG CRD is reconciled asynchronously)
		var srv mssqlv1.SQLServer
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, srvKey, &srv); err != nil {
				return false, nil
			}
			return srv.Status.PrimaryReplica != "", nil
		})
		if err != nil {
			t.Errorf("PrimaryReplica was not set (AG CRD may still be provisioning)")
		}

		if srv.Status.ServerVersion == "" {
			t.Error("Expected serverVersion to be set")
		}
		if srv.Status.ReadyReplicas == nil || *srv.Status.ReadyReplicas != 2 {
			t.Errorf("Expected readyReplicas=2, got %v", srv.Status.ReadyReplicas)
		}
		t.Logf("Managed cluster SQLServer ready: version=%s primary=%s readyReplicas=%v",
			srv.Status.ServerVersion, srv.Status.PrimaryReplica, srv.Status.ReadyReplicas)
	})

	t.Run("AGCRDCreatedWithAutoFailover", func(t *testing.T) {
		// Verify the managed controller created an AvailabilityGroup CRD
		// with autoFailover enabled. The actual failover behavior is tested
		// separately in TestE2EAutoFailover (using pods without PVC for
		// reliable SHUTDOWN detection).
		agCRKey := types.NamespacedName{Name: srvKey.Name + "-ag", Namespace: testNamespace}
		var agCR mssqlv1.AvailabilityGroup
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, agCRKey, &agCR); err != nil {
				return false, nil
			}
			return agCR.Status.PrimaryReplica != "", nil
		})
		if err != nil {
			t.Fatalf("AG CRD not found or primary not set")
		}

		if agCR.Spec.AutoFailover == nil || !*agCR.Spec.AutoFailover {
			t.Error("Expected autoFailover=true on AG CRD")
		}
		if agCR.Spec.HealthCheckInterval == nil || *agCR.Spec.HealthCheckInterval != "5s" {
			t.Errorf("Expected healthCheckInterval=5s, got %v", agCR.Spec.HealthCheckInterval)
		}
		if agCR.Spec.ClusterType == nil || *agCR.Spec.ClusterType != "None" {
			t.Errorf("Expected clusterType=None, got %v", agCR.Spec.ClusterType)
		}
		t.Logf("AG CRD verified: name=%s primary=%s autoFailover=true healthCheck=5s",
			agCRKey.Name, agCR.Status.PrimaryReplica)
	})

	t.Run("Cleanup", func(t *testing.T) {
		_ = k8sClient.Delete(ctx, &mssqlv1.SQLServer{
			ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace},
		})
		waitForDeletion(t, srvKey, &mssqlv1.SQLServer{}, 2*time.Minute)

		// Verify cascade deletion
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}, &sts)
			return errors.IsNotFound(err), nil
		})
		if err != nil {
			t.Error("StatefulSet was not garbage collected after cluster SQLServer deletion")
		}

		// Verify cert secrets are cleaned up via owner references
		for _, name := range []string{srvKey.Name + "-ca", srvKey.Name + "-cert-0", srvKey.Name + "-cert-1"} {
			err := wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
				var s corev1.Secret
				err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: srvKey.Namespace}, &s)
				return errors.IsNotFound(err), nil
			})
			if err != nil {
				t.Errorf("Certificate secret %s was not garbage collected", name)
			}
		}
		t.Log("Managed cluster SQLServer and all child resources cleaned up")
	})
}
