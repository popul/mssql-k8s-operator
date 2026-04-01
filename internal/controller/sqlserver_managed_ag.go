package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
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

	// Determine the credentials secret for the AG replicas.
	// In managed mode without credentialsSecret, create a credentials secret
	// from the saPasswordSecret so the AG controller can connect.
	credsSecretName := ""
	if srv.Spec.CredentialsSecret != nil {
		credsSecretName = srv.Spec.CredentialsSecret.Name
	} else if inst.SAPasswordSecret.Name != "" {
		// Create a standard credentials secret from saPasswordSecret
		credsSecretName = srv.Name + "-sa-credentials"
		if err := r.ensureSACredentialsSecret(ctx, srv, credsSecretName); err != nil {
			return fmt.Errorf("failed to ensure SA credentials secret: %w", err)
		}
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

// ensureSACredentialsSecret creates a standard credentials secret (username/password)
// from the SA password secret so the AG controller can use getCredentialsFromSecret.
func (r *SQLServerReconciler) ensureSACredentialsSecret(ctx context.Context, srv *v1alpha1.SQLServer, secretName string) error {
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: srv.Namespace}, &existing)
	if err == nil {
		return nil // Already exists
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	// Read the SA password
	_, password, err := getCredentialsFromSAPasswordSecret(ctx, r.Client, srv.Namespace, srv.Spec.Instance.SAPasswordSecret.Name)
	if err != nil {
		return fmt.Errorf("failed to read SA password: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: srv.Namespace,
			Labels:    instanceLabels(srv),
		},
		Data: map[string][]byte{
			"username": []byte("sa"),
			"password": []byte(password),
		},
	}
	if err := controllerutil.SetControllerReference(srv, secret, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, secret)
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }
func containsDot(s string) bool  { return strings.Contains(s, ".") }
