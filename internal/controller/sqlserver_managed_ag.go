package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

// reconcileManagedAG ensures an AvailabilityGroup CRD exists for the managed SQL Server cluster.
// The AG lifecycle (creation, join, failover) is fully delegated to the AvailabilityGroupReconciler.
func (r *SQLServerReconciler) reconcileManagedAG(ctx context.Context, srv *v1alpha1.SQLServer) error {
	logger := log.FromContext(ctx)
	inst := srv.Spec.Instance

	replicas := int32(1)
	if inst.Replicas != nil {
		replicas = *inst.Replicas
	}
	if replicas <= 1 {
		return nil
	}

	ag := inst.AvailabilityGroup
	if ag == nil {
		return nil
	}

	agName := srv.Name + "-ag"
	if ag.AGName != nil {
		agName = *ag.AGName
	}

	agCRName := srv.Name + "-ag"

	// Check if the AG CRD already exists
	var existingAG v1alpha1.AvailabilityGroup
	err := r.Get(ctx, types.NamespacedName{Name: agCRName, Namespace: srv.Namespace}, &existingAG)
	if err == nil {
		// AG CRD already exists — read its status to update SQLServer status.
		// PrimaryReplica from AG status is the pod name (@@SERVERNAME).
		// Map it to FQDN for the operator to connect.
		if existingAG.Status.PrimaryReplica != "" {
			srv.Status.PrimaryReplica = existingAG.Status.PrimaryReplica
			// If it's a pod name (no dots), build the FQDN
			if !containsDot(existingAG.Status.PrimaryReplica) {
				srv.Status.PrimaryReplica = fmt.Sprintf("%s.%s-headless.%s.svc.cluster.local",
					existingAG.Status.PrimaryReplica, srv.Name, srv.Namespace)
			}
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check AG CRD: %w", err)
	}

	// Build the AvailabilityGroup CR
	logger.Info("creating AvailabilityGroup CRD for managed cluster", "agName", agName, "replicas", replicas)

	credsSecretName := ""
	if srv.Spec.CredentialsSecret != nil {
		credsSecretName = srv.Spec.CredentialsSecret.Name
	}

	availabilityMode := v1alpha1.AvailabilityModeSynchronous
	if ag.AvailabilityMode != nil && *ag.AvailabilityMode == "AsynchronousCommit" {
		availabilityMode = v1alpha1.AvailabilityModeAsynchronous
	}

	agReplicas := make([]v1alpha1.AGReplicaSpec, replicas)
	for i := int32(0); i < replicas; i++ {
		host := replicaHost(srv, int(i))
		podName := fmt.Sprintf("%s-%d", srv.Name, i)
		agReplicas[i] = v1alpha1.AGReplicaSpec{
			ServerName:       podName,
			EndpointURL:      fmt.Sprintf("TCP://%s:%d", host, hadrEndpointPort),
			AvailabilityMode: availabilityMode,
			FailoverMode:     v1alpha1.FailoverModeManual,
			SeedingMode:      v1alpha1.SeedingModeAutomatic,
			SecondaryRole:    v1alpha1.SecondaryRoleAllowAll,
			Server: v1alpha1.ServerReference{
				Host:              host,
				Port:              srv.Spec.Port,
				CredentialsSecret: v1alpha1.SecretReference{Name: credsSecretName},
			},
		}
	}

	clusterType := "None"
	agCR := &v1alpha1.AvailabilityGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agCRName,
			Namespace: srv.Namespace,
		},
		Spec: v1alpha1.AvailabilityGroupSpec{
			AGName:                    agName,
			Replicas:                  agReplicas,
			AutomatedBackupPreference: stringPtr("Secondary"),
			DBFailover:                boolPtr(false),
			ClusterType:               &clusterType,
			AutoFailover:              ag.AutoFailover,
			HealthCheckInterval:       ag.HealthCheckInterval,
			FailoverCooldown:          ag.FailoverCooldown,
		},
	}

	// Set owner reference so AG CRD is deleted when SQLServer is deleted
	if err := controllerutil.SetControllerReference(srv, agCR, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on AG CRD: %w", err)
	}

	if err := r.Create(ctx, agCR); err != nil {
		return fmt.Errorf("failed to create AvailabilityGroup CRD: %w", err)
	}

	r.Recorder.Event(srv, "Normal", "AGCRDCreated",
		fmt.Sprintf("AvailabilityGroup CRD %s created for managed cluster", agCRName))
	logger.Info("AvailabilityGroup CRD created", "name", agCRName, "agName", agName)
	return nil
}

func stringPtr(s string) *string       { return &s }
func boolPtr(b bool) *bool             { return &b }
func containsDot(s string) bool        { return strings.Contains(s, ".") }
