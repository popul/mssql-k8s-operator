//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

// =============================================================================
// AvailabilityGroup & AGFailover E2E Tests
// =============================================================================
//
// AG tests require multiple SQL Server instances with HADR enabled.
// They are skipped in the standard e2e suite (single-instance) and require
// the E2E_AG_ENABLED=true environment variable plus a multi-instance setup.
//
// To run AG e2e tests:
//   1. Deploy 2+ SQL Server Enterprise instances with HADR enabled
//   2. Set E2E_AG_ENABLED=true
//   3. Set E2E_SQL_HOST_0, E2E_SQL_HOST_1 for the replica hostnames
//   4. Set E2E_AG_CREDS_SECRET for the credentials secret name

func TestE2EAvailabilityGroup(t *testing.T) {
	host0 := fmt.Sprintf("sql-0.sql-headless.%s.svc.cluster.local", testNamespace)
	host1 := fmt.Sprintf("sql-1.sql-headless.%s.svc.cluster.local", testNamespace)
	credsSecret := "mssql-sa-credentials"

	// Deploy 2 SQL Server instances with HADR enabled
	deployAGInfrastructure(t)
	setupAGCertificates(t)

	agKey := types.NamespacedName{Name: "test-ag", Namespace: testNamespace}

	t.Run("CreateAG", func(t *testing.T) {
		ag := &mssqlv1.AvailabilityGroup{
			ObjectMeta: metav1.ObjectMeta{Name: agKey.Name, Namespace: agKey.Namespace},
			Spec: mssqlv1.AvailabilityGroupSpec{
				AGName: "e2eag",
				Replicas: []mssqlv1.AGReplicaSpec{
					{
						ServerName:       "sql-0",
						EndpointURL:      fmt.Sprintf("TCP://%s:5022", host0),
						AvailabilityMode: mssqlv1.AvailabilityModeSynchronous,
						FailoverMode:     mssqlv1.FailoverModeManual,
						SeedingMode:      mssqlv1.SeedingModeAutomatic,
						Server: mssqlv1.ServerReference{
							Host:              host0,
							Port:              ptr(int32(1433)),
							CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
						},
					},
					{
						ServerName:       "sql-1",
						EndpointURL:      fmt.Sprintf("TCP://%s:5022", host1),
						AvailabilityMode: mssqlv1.AvailabilityModeSynchronous,
						FailoverMode:     mssqlv1.FailoverModeManual,
						SeedingMode:      mssqlv1.SeedingModeAutomatic,
						Server: mssqlv1.ServerReference{
							Host:              host1,
							Port:              ptr(int32(1433)),
							CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
						},
					},
				},
				AutomatedBackupPreference: ptr("Secondary"),
				DBFailover:                ptr(false),
				ClusterType:               ptr("None"),
			},
		}
		if err := k8sClient.Create(ctx, ag); err != nil {
			t.Fatalf("Failed to create AvailabilityGroup CR: %v", err)
		}

		// With CLUSTER_TYPE=NONE and no databases, AG is "not healthy" but functional.
		// Wait for the controller to set status with primary and replica info.
		var updated mssqlv1.AvailabilityGroup
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, agKey, &updated); err != nil {
				return false, nil
			}
			return updated.Status.PrimaryReplica != "" && len(updated.Status.Replicas) == 2, nil
		})
		if err != nil {
			t.Fatalf("AG did not reach expected state: primary=%q replicas=%d",
				updated.Status.PrimaryReplica, len(updated.Status.Replicas))
		}

		// Verify both replicas are connected
		for _, r := range updated.Status.Replicas {
			if !r.Connected {
				t.Errorf("Replica %s is not connected", r.ServerName)
			}
		}
		t.Logf("AG created: primary=%s, replicas=%d", updated.Status.PrimaryReplica, len(updated.Status.Replicas))
	})

	t.Run("ManualFailover", func(t *testing.T) {
		foKey := types.NamespacedName{Name: "test-ag-failover", Namespace: testNamespace}
		failover := &mssqlv1.AGFailover{
			ObjectMeta: metav1.ObjectMeta{Name: foKey.Name, Namespace: foKey.Namespace},
			Spec: mssqlv1.AGFailoverSpec{
				AGName:        "e2eag",
				TargetReplica: "sql-1",
				Force:         ptr(true), // Force failover — CLUSTER_TYPE=NONE may not have full sync
				Server: mssqlv1.ServerReference{
					Host:              host1,
					Port:              ptr(int32(1433)),
					CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
				},
			},
		}
		if err := k8sClient.Create(ctx, failover); err != nil {
			t.Fatalf("Failed to create AGFailover CR: %v", err)
		}

		// Wait for failover to complete
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var fo mssqlv1.AGFailover
			if err := k8sClient.Get(ctx, foKey, &fo); err != nil {
				return false, nil
			}
			return fo.Status.Phase == mssqlv1.FailoverPhaseCompleted || fo.Status.Phase == mssqlv1.FailoverPhaseFailed, nil
		})
		if err != nil {
			t.Fatalf("Timed out waiting for AGFailover to complete")
		}

		var fo mssqlv1.AGFailover
		if err := k8sClient.Get(ctx, foKey, &fo); err != nil {
			t.Fatalf("Failed to get AGFailover: %v", err)
		}
		if fo.Status.Phase != mssqlv1.FailoverPhaseCompleted {
			t.Errorf("Expected AGFailover phase=Completed, got %s", fo.Status.Phase)
		}
		if fo.Status.NewPrimary != "sql-1" {
			t.Errorf("Expected newPrimary=sql-1, got %s", fo.Status.NewPrimary)
		}

		// Cleanup failover CR
		_ = k8sClient.Delete(ctx, failover)
	})

	// Cleanup AG
	var ag mssqlv1.AvailabilityGroup
	if err := k8sClient.Get(ctx, agKey, &ag); err == nil {
		_ = k8sClient.Delete(ctx, &ag)
	}
}

func TestE2EAutoFailover(t *testing.T) {
	host0 := fmt.Sprintf("sql-0.sql-headless.%s.svc.cluster.local", testNamespace)
	host1 := fmt.Sprintf("sql-1.sql-headless.%s.svc.cluster.local", testNamespace)
	credsSecret := "mssql-sa-credentials"

	// Reuse AG infrastructure from previous test (or deploy if not already running)
	deployAGInfrastructure(t)
	setupAGCertificates(t)

	agKey := types.NamespacedName{Name: "test-auto-ag", Namespace: testNamespace}

	ag := &mssqlv1.AvailabilityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: agKey.Name, Namespace: agKey.Namespace},
		Spec: mssqlv1.AvailabilityGroupSpec{
			AGName: "autoag",
			Replicas: []mssqlv1.AGReplicaSpec{
				{
					ServerName:       "sql-0",
					EndpointURL:      fmt.Sprintf("TCP://%s:5022", host0),
					AvailabilityMode: mssqlv1.AvailabilityModeSynchronous,
					FailoverMode:     mssqlv1.FailoverModeManual,
					SeedingMode:      mssqlv1.SeedingModeAutomatic,
					Server: mssqlv1.ServerReference{
						Host:              host0,
						Port:              ptr(int32(1433)),
						CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
					},
				},
				{
					ServerName:       "sql-1",
					EndpointURL:      fmt.Sprintf("TCP://%s:5022", host1),
					AvailabilityMode: mssqlv1.AvailabilityModeSynchronous,
					FailoverMode:     mssqlv1.FailoverModeManual,
					SeedingMode:      mssqlv1.SeedingModeAutomatic,
					Server: mssqlv1.ServerReference{
						Host:              host1,
						Port:              ptr(int32(1433)),
						CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
					},
				},
			},
			AutomatedBackupPreference: ptr("Secondary"),
			DBFailover:                ptr(false),
			ClusterType:               ptr("None"),
			AutoFailover:              ptr(true),
			HealthCheckInterval:       ptr("5s"),
			FailoverCooldown:          ptr("30s"),
		},
	}
	if err := k8sClient.Create(ctx, ag); err != nil {
		t.Fatalf("Failed to create AG CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.AvailabilityGroup{
			ObjectMeta: metav1.ObjectMeta{Name: agKey.Name, Namespace: agKey.Namespace},
		})
	}()

	// Wait for AG to have both replicas connected
	var updated mssqlv1.AvailabilityGroup
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, agKey, &updated); err != nil {
			return false, nil
		}
		return updated.Status.PrimaryReplica != "" && len(updated.Status.Replicas) == 2, nil
	})
	if err != nil {
		t.Fatalf("AG did not reach expected state: primary=%q replicas=%d",
			updated.Status.PrimaryReplica, len(updated.Status.Replicas))
	}
	t.Logf("AG ready: primary=%s", updated.Status.PrimaryReplica)

	// Stop SQL Server inside the primary pod (SHUTDOWN WITH NOWAIT).
	// The container restarts, but during the ~15s restart window,
	// the operator detects the failure and triggers auto-failover.
	t.Log("Shutting down SQL Server on primary sql-0...")
	cmd := exec.CommandContext(ctx, "kubectl", "exec", "sql-0", "-n", testNamespace, "--",
		"/opt/mssql-tools18/bin/sqlcmd", "-S", "localhost", "-U", "sa", "-P", saPassword,
		"-Q", "SHUTDOWN WITH NOWAIT", "-C", "-No")
	_ = cmd.Run() // ignore error — connection drops on shutdown

	// Wait for auto-failover: sql-1 should become primary
	t.Log("Waiting for auto-failover...")
	err = wait.PollUntilContextTimeout(ctx, 3*time.Second, 120*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, agKey, &updated); err != nil {
			return false, nil
		}
		return updated.Status.PrimaryReplica == "sql-1", nil
	})
	if err != nil {
		t.Fatalf("Auto-failover did not happen: primary is still %q, autoFailoverCount=%d",
			updated.Status.PrimaryReplica, updated.Status.AutoFailoverCount)
	}

	t.Logf("Auto-failover completed: new primary=%s, count=%d",
		updated.Status.PrimaryReplica, updated.Status.AutoFailoverCount)

	if updated.Status.LastAutoFailoverTime == nil {
		t.Error("Expected lastAutoFailoverTime to be set")
	}
	if updated.Status.AutoFailoverCount < 1 {
		t.Errorf("Expected autoFailoverCount >= 1, got %d", updated.Status.AutoFailoverCount)
	}
}
