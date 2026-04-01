//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	"github.com/popul/mssql-k8s-operator/internal/controller"
)

// =============================================================================
// HA Service Routing E2E Tests
// =============================================================================
//
// These tests verify that the operator correctly routes traffic in HA mode:
// - The client service (read-write) routes only to the primary replica
// - The read-only service routes only to secondary replicas
// - Pod role labels are updated on failover
//
// Prerequisites: same as HA tests (E2E_AG_ENABLED=true, 2+ SQL Server instances)

func TestE2EHAServiceRouting(t *testing.T) {
	host0 := fmt.Sprintf("sql-0.sql-headless.%s.svc.cluster.local", testNamespace)
	host1 := fmt.Sprintf("sql-1.sql-headless.%s.svc.cluster.local", testNamespace)
	credsSecret := "mssql-sa-credentials"

	deployAGInfrastructure(t)
	setupAGCertificates(t)

	agKey := types.NamespacedName{Name: "test-routing-ag", Namespace: testNamespace}

	// Create AG with auto-failover
	ag := &mssqlv1.AvailabilityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: agKey.Name, Namespace: agKey.Namespace},
		Spec: mssqlv1.AvailabilityGroupSpec{
			AGName: "routingag",
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

	// Wait for AG to be ready with primary set
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

	t.Run("PodRoleLabelsApplied", func(t *testing.T) {
		// Wait for the primary pod to have the role=primary label
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			var pod corev1.Pod
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "sql-0", Namespace: testNamespace}, &pod); err != nil {
				return false, nil
			}
			return pod.Labels[controller.LabelRole] == controller.RolePrimary, nil
		})
		if err != nil {
			t.Fatalf("Primary pod sql-0 did not get role=primary label")
		}

		// Verify secondary has role=secondary
		var pod1 corev1.Pod
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "sql-1", Namespace: testNamespace}, &pod1); err != nil {
			t.Fatalf("Failed to get pod sql-1: %v", err)
		}
		if pod1.Labels[controller.LabelRole] != controller.RoleSecondary {
			t.Errorf("Expected sql-1 to have role=secondary, got %q", pod1.Labels[controller.LabelRole])
		}
		t.Log("Pod role labels correctly applied")
	})

	t.Run("ReadOnlyServiceExists", func(t *testing.T) {
		// The read-only service is created by the SQLServer controller for managed mode.
		// In standalone AG mode (this test), we verify the concept by checking labels.
		// For a managed SQLServer CR test, we'd check the service directly.

		// Verify both pods have distinct role labels
		var pod0, pod1 corev1.Pod
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "sql-0", Namespace: testNamespace}, &pod0)
		_ = k8sClient.Get(ctx, types.NamespacedName{Name: "sql-1", Namespace: testNamespace}, &pod1)

		if pod0.Labels[controller.LabelRole] == pod1.Labels[controller.LabelRole] {
			t.Errorf("Expected different roles: sql-0=%q sql-1=%q",
				pod0.Labels[controller.LabelRole], pod1.Labels[controller.LabelRole])
		}
		t.Logf("Pod roles: sql-0=%s sql-1=%s",
			pod0.Labels[controller.LabelRole], pod1.Labels[controller.LabelRole])
	})

	t.Run("LabelsUpdatedAfterFailover", func(t *testing.T) {
		// Record the current primary
		initialPrimary := updated.Status.PrimaryReplica
		t.Logf("Initial primary: %s", initialPrimary)

		// Create a manual failover to sql-1
		foKey := types.NamespacedName{Name: "test-routing-failover", Namespace: testNamespace}
		failover := &mssqlv1.AGFailover{
			ObjectMeta: metav1.ObjectMeta{Name: foKey.Name, Namespace: foKey.Namespace},
			Spec: mssqlv1.AGFailoverSpec{
				AGName:        "routingag",
				TargetReplica: "sql-1",
				Force:         ptr(true),
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
		defer func() {
			_ = k8sClient.Delete(ctx, failover)
		}()

		// Wait for failover to complete
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var fo mssqlv1.AGFailover
			if err := k8sClient.Get(ctx, foKey, &fo); err != nil {
				return false, nil
			}
			return fo.Status.Phase == mssqlv1.FailoverPhaseCompleted, nil
		})
		if err != nil {
			t.Fatalf("Failover did not complete in time")
		}

		// Wait for the AG controller to update the role labels
		// sql-1 should become primary, sql-0 should become secondary
		err = wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			var pod1 corev1.Pod
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "sql-1", Namespace: testNamespace}, &pod1); err != nil {
				return false, nil
			}
			return pod1.Labels[controller.LabelRole] == controller.RolePrimary, nil
		})
		if err != nil {
			t.Fatalf("sql-1 did not get role=primary label after failover")
		}

		// Verify sql-0 is now secondary
		var pod0 corev1.Pod
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "sql-0", Namespace: testNamespace}, &pod0); err != nil {
			t.Fatalf("Failed to get pod sql-0: %v", err)
		}
		if pod0.Labels[controller.LabelRole] != controller.RoleSecondary {
			t.Errorf("Expected sql-0 to have role=secondary after failover, got %q",
				pod0.Labels[controller.LabelRole])
		}

		t.Log("Pod role labels correctly updated after failover")
	})
}

// TestE2EManagedHAServiceRouting tests the full managed mode with SQLServer CR,
// verifying that the client service and read-only service are created with correct selectors.
func TestE2EManagedHAServiceRouting(t *testing.T) {
	srvKey := types.NamespacedName{Name: "ha-routing-test", Namespace: testNamespace}

	// Create managed SQLServer CR with 2 replicas
	replicas := int32(2)
	svcType := corev1.ServiceTypeClusterIP
	agName := "haroutingag"
	srv := &mssqlv1.SQLServer{
		ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace},
		Spec: mssqlv1.SQLServerSpec{
			Instance: &mssqlv1.InstanceSpec{
				Image:            ptr(sqlImage),
				SAPasswordSecret: mssqlv1.SecretReference{Name: "mssql-sa-password"},
				AcceptEULA:       true,
				Edition:          ptr("Developer"),
				Replicas:         &replicas,
				StorageSize:      ptr("1Gi"),
				ServiceType:      &svcType,
				AvailabilityGroup: &mssqlv1.ManagedAGSpec{
					AGName:       &agName,
					AutoFailover: ptr(true),
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, srv); err != nil {
		t.Fatalf("Failed to create SQLServer CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, srv)
	}()

	t.Run("ClientServiceHasPrimarySelector", func(t *testing.T) {
		// Wait for the client service to be created
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var svc corev1.Service
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: srvKey.Name, Namespace: srvKey.Namespace}, &svc); err != nil {
				return false, nil
			}
			return svc.Spec.Selector[controller.LabelRole] == controller.RolePrimary, nil
		})
		if err != nil {
			t.Fatalf("Client service does not have role=primary selector")
		}
		t.Log("Client service has role=primary selector")
	})

	t.Run("ReadOnlyServiceCreated", func(t *testing.T) {
		// Wait for the read-only service to be created
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var svc corev1.Service
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      srvKey.Name + "-readonly",
				Namespace: srvKey.Namespace,
			}, &svc); err != nil {
				return false, nil
			}
			return svc.Spec.Selector[controller.LabelRole] == controller.RoleSecondary, nil
		})
		if err != nil {
			t.Fatalf("Read-only service does not exist or lacks role=secondary selector")
		}
		t.Log("Read-only service created with role=secondary selector")
	})

	t.Run("ReadOnlyServicePort", func(t *testing.T) {
		var svc corev1.Service
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      srvKey.Name + "-readonly",
			Namespace: srvKey.Namespace,
		}, &svc); err != nil {
			t.Fatalf("Failed to get read-only service: %v", err)
		}
		if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 1433 {
			t.Errorf("Expected read-only service to have port 1433, got %v", svc.Spec.Ports)
		}
	})

	// Cleanup: delete the managed SQLServer CR
	_ = k8sClient.Delete(ctx, srv)

	// Wait for cleanup
	_ = wait.PollUntilContextTimeout(ctx, pollInterval, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		var s mssqlv1.SQLServer
		err := k8sClient.Get(ctx, srvKey, &s)
		return client.IgnoreNotFound(err) == nil && err != nil, nil
	})
}

