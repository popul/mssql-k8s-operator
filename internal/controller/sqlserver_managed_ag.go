package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// reconcileManagedAG creates the Availability Group and joins secondaries.
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

	// Read credentials
	secretNS := srv.Namespace
	if srv.Spec.CredentialsSecret != nil && srv.Spec.CredentialsSecret.Namespace != nil {
		secretNS = *srv.Spec.CredentialsSecret.Namespace
	}
	if srv.Spec.CredentialsSecret == nil {
		return fmt.Errorf("credentialsSecret is required")
	}

	username, password, err := getCredentialsFromSecret(ctx, r.Client, secretNS, srv.Spec.CredentialsSecret.Name)
	if err != nil {
		return fmt.Errorf("failed to read credentials: %w", err)
	}

	port := int32(sqlPort)
	if srv.Spec.Port != nil {
		port = *srv.Spec.Port
	}
	tlsEnabled := srv.Spec.TLS != nil && *srv.Spec.TLS

	// Connect to primary (pod-0)
	primaryHost := replicaHost(srv, 0)
	primaryConn, err := r.SQLClientFactory(primaryHost, int(port), username, password, tlsEnabled)
	if err != nil {
		return fmt.Errorf("failed to connect to primary (%s): %w", primaryHost, err)
	}
	defer primaryConn.Close()

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	// Check if AG already exists
	agExists, err := primaryConn.AGExists(sqlCtx, agName)
	if err != nil {
		return fmt.Errorf("failed to check AG existence: %w", err)
	}

	if !agExists {
		logger.Info("creating availability group", "agName", agName)

		availabilityMode := "SYNCHRONOUS_COMMIT"
		if ag.AvailabilityMode != nil && *ag.AvailabilityMode == "AsynchronousCommit" {
			availabilityMode = "ASYNCHRONOUS_COMMIT"
		}

		clusterType := "NONE"

		agReplicas := make([]sqlclient.AGReplicaConfig, replicas)
		for i := int32(0); i < replicas; i++ {
			host := replicaHost(srv, int(i))
			agReplicas[i] = sqlclient.AGReplicaConfig{
				ServerName:       host,
				EndpointURL:      fmt.Sprintf("TCP://%s:%d", host, hadrEndpointPort),
				AvailabilityMode: availabilityMode,
				FailoverMode:     "MANUAL", // Required for CLUSTER_TYPE=NONE
				SeedingMode:      "AUTOMATIC",
				SecondaryRole:    "ALL",
			}
		}

		config := &sqlclient.AGConfig{
			Name:                      agName,
			Replicas:                  agReplicas,
			ClusterType:               clusterType,
			AutomatedBackupPreference: "SECONDARY",
			DBFailover:                false,
		}

		if err := primaryConn.CreateAG(sqlCtx, config); err != nil {
			return fmt.Errorf("failed to create availability group: %w", err)
		}
		r.Recorder.Event(srv, corev1.EventTypeNormal, "AGCreated",
			fmt.Sprintf("Availability Group %s created with %d replicas", agName, replicas))
	}

	// Join secondaries
	for i := int32(1); i < replicas; i++ {
		secondaryHost := replicaHost(srv, int(i))
		secondaryConn, err := r.SQLClientFactory(secondaryHost, int(port), username, password, tlsEnabled)
		if err != nil {
			logger.Error(err, "failed to connect to secondary", "replica", i, "host", secondaryHost)
			continue
		}

		joinCtx, joinCancel := sqlContext(ctx)

		// Check if already part of an AG
		role, err := secondaryConn.GetAGReplicaRole(joinCtx, agName, secondaryHost)
		if err != nil || role == "" {
			logger.Info("joining secondary to AG", "replica", i, "agName", agName)
			if err := secondaryConn.JoinAG(joinCtx, agName, "NONE"); err != nil {
				logger.Error(err, "failed to join secondary to AG", "replica", i)
				joinCancel()
				secondaryConn.Close()
				continue
			}
			// Grant AG create database for automatic seeding
			if err := secondaryConn.GrantAGCreateDatabase(joinCtx, agName); err != nil {
				logger.Error(err, "failed to grant AG create database", "replica", i)
			}
			r.Recorder.Event(srv, corev1.EventTypeNormal, "ReplicaJoined",
				fmt.Sprintf("Replica %s joined AG %s", secondaryHost, agName))
		}

		joinCancel()
		secondaryConn.Close()
	}

	// Add databases to AG
	if len(ag.Databases) > 0 {
		dbCtx, dbCancel := sqlContext(ctx)
		defer dbCancel()

		for _, db := range ag.Databases {
			if err := primaryConn.AddDatabaseToAG(dbCtx, agName, db); err != nil {
				logger.Error(err, "failed to add database to AG", "database", db, "ag", agName)
			}
		}
	}

	// Update status with AG info
	agStatus, err := primaryConn.GetAGStatus(sqlCtx, agName)
	if err != nil {
		logger.Error(err, "failed to get AG status")
	} else if agStatus != nil {
		srv.Status.PrimaryReplica = agStatus.PrimaryReplica
	}

	return nil
}
