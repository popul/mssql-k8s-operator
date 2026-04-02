package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/time/rate"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// AvailabilityGroupReconciler reconciles an AvailabilityGroup object.
type AvailabilityGroupReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=availabilitygroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=availabilitygroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=availabilitygroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=agfailovers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch

func (r *AvailabilityGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("AvailabilityGroup").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch the AvailabilityGroup CR
	var ag v1alpha1.AvailabilityGroup
	if err := r.Get(ctx, req.NamespacedName, &ag); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer handling
	if ag.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &ag)
	}

	if !controllerutil.ContainsFinalizer(&ag, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&ag, v1alpha1.Finalizer)
		if err := r.Update(ctx, &ag); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Auto-failover settings
	autoFailoverEnabled := ag.Spec.AutoFailover != nil && *ag.Spec.AutoFailover
	healthCheckInterval := 10 * time.Second
	if ag.Spec.HealthCheckInterval != nil {
		if d, err := time.ParseDuration(*ag.Spec.HealthCheckInterval); err == nil {
			healthCheckInterval = d
		}
	}
	requeue := requeueInterval
	if autoFailoverEnabled {
		requeue = healthCheckInterval
	}

	// 4. Connect to the primary replica (known from status, fallback to first in spec)
	primaryReplica := ag.Spec.Replicas[0]
	if ag.Status.PrimaryReplica != "" {
		for i := range ag.Spec.Replicas {
			if ag.Spec.Replicas[i].ServerName == ag.Status.PrimaryReplica {
				primaryReplica = ag.Spec.Replicas[i]
				break
			}
		}
	}
	username, password, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, primaryReplica.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &ag, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", primaryReplica.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &ag, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	primaryClient, err := connectToSQL(primaryReplica.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to primary replica")
		r.Recorder.Event(&ag, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())

		if autoFailoverEnabled {
			result, err := r.handleAutoFailover(ctx, &ag, requeue)
			if err != nil {
				return ctrl.Result{RequeueAfter: requeueWithJitter(requeue)}, nil
			}
			return result, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to connect to primary replica: %w", err)
	}
	defer primaryClient.Close()

	// Verify primary is actually reachable (sql.Open doesn't connect)
	if autoFailoverEnabled {
		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := primaryClient.Ping(pingCtx); err != nil {
			pingCancel()
			primaryClient.Close()
			logger.Info("primary replica unreachable (ping failed)", "error", err)
			result, autoErr := r.handleAutoFailover(ctx, &ag, requeue)
			if autoErr != nil {
				return ctrl.Result{RequeueAfter: requeueWithJitter(requeue)}, nil
			}
			return result, nil
		}
		pingCancel()
	}

	// 4b. Fencing: detect and resolve split-brain
	if ag.Status.PrimaryReplica != "" {
		previousPrimary := ag.Status.PrimaryReplica
		fenced, fenceErr := r.detectAndResolveSplitBrain(ctx, &ag)
		if fenceErr != nil {
			logger.Error(fenceErr, "fencing check failed")
		}
		if fenced {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if ag.Status.PrimaryReplica != previousPrimary {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// 5. Ensure HADR endpoint exists on primary
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	endpointExists, err := primaryClient.HADREndpointExists(sqlCtx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check HADR endpoint: %w", err)
	}
	if !endpointExists {
		sqlCtx2, cancel2 := sqlContext(ctx)
		defer cancel2()
		if err := primaryClient.CreateHADREndpoint(sqlCtx2, 5022); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create HADR endpoint: %w", err)
		}
		r.Recorder.Event(&ag, corev1.EventTypeNormal, "HADREndpointCreated", "HADR endpoint created on primary")
	}

	// 5. Check if AG exists
	sqlCtx3, cancel3 := sqlContext(ctx)
	defer cancel3()
	agExists, err := primaryClient.AGExists(sqlCtx3, ag.Spec.AGName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check AG existence: %w", err)
	}

	// 6. Create AG if it doesn't exist
	if !agExists {
		if err := r.createAG(ctx, &ag, primaryClient); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create availability group: %w", err)
		}
		r.Recorder.Event(&ag, corev1.EventTypeNormal, "AvailabilityGroupCreated",
			fmt.Sprintf("Availability Group %s created", ag.Spec.AGName))
		logger.Info("availability group created", "ag", ag.Spec.AGName)

		// Join secondary replicas
		if err := r.joinSecondaries(ctx, &ag); err != nil {
			logger.Error(err, "failed to join some secondary replicas")
			r.Recorder.Event(&ag, corev1.EventTypeWarning, "SecondaryJoinFailed", err.Error())
			// Don't fail — requeue and retry
		}
	}

	// 7. Reconcile databases in the AG
	if err := r.reconcileDatabases(ctx, &ag, primaryClient); err != nil {
		logger.Error(err, "failed to reconcile databases in AG")
	}

	// 8. Reconcile listener
	if err := r.reconcileListener(ctx, &ag, primaryClient); err != nil {
		logger.Error(err, "failed to reconcile listener")
	}

	// 9. Observe AG status and update CR status
	sqlCtx4, cancel4 := sqlContext(ctx)
	defer cancel4()
	agStatus, err := primaryClient.GetAGStatus(sqlCtx4, ag.Spec.AGName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get AG status: %w", err)
	}

	if err := r.updateAGStatus(ctx, &ag, agStatus); err != nil {
		return ctrl.Result{}, err
	}

	// Rejoin disconnected/orphan secondaries
	agStatusMap := make(map[string]sqlclient.AGReplicaState)
	for _, rs := range agStatus.Replicas {
		agStatusMap[rs.ServerName] = rs
	}
	for _, specReplica := range ag.Spec.Replicas {
		if specReplica.ServerName == agStatus.PrimaryReplica {
			continue
		}
		rs, found := agStatusMap[specReplica.ServerName]
		if found && rs.Connected {
			continue
		}
		r.tryRejoinReplica(ctx, &ag, specReplica)
	}

	// Keep pod role labels in sync with actual AG state
	if agStatus.PrimaryReplica != "" {
		r.updateReplicaRoleLabelsFromAG(ctx, &ag, agStatus.PrimaryReplica)
	}

	opmetrics.ReconcileTotal.WithLabelValues("AvailabilityGroup", "success").Inc()
	return ctrl.Result{RequeueAfter: requeueWithJitter(requeue)}, nil
}

func (r *AvailabilityGroupReconciler) createAG(ctx context.Context, ag *v1alpha1.AvailabilityGroup, primaryClient sqlclient.SQLClient) error {
	config := sqlclient.AGConfig{
		Name:                      ag.Spec.AGName,
		AutomatedBackupPreference: "SECONDARY",
		DBFailover:                true,
	}
	if ag.Spec.AutomatedBackupPreference != nil {
		config.AutomatedBackupPreference = mapBackupPreference(*ag.Spec.AutomatedBackupPreference)
	}
	if ag.Spec.DBFailover != nil {
		config.DBFailover = *ag.Spec.DBFailover
	}
	if ag.Spec.ClusterType != nil {
		config.ClusterType = mapClusterType(*ag.Spec.ClusterType)
	}

	for _, db := range ag.Spec.Databases {
		config.Databases = append(config.Databases, db.Name)
	}

	for i := range ag.Spec.Replicas {
		config.Replicas = append(config.Replicas, sqlclient.AGReplicaConfig{
			ServerName:       ag.Spec.Replicas[i].ServerName,
			EndpointURL:      ag.Spec.Replicas[i].EndpointURL,
			AvailabilityMode: mapAvailabilityMode(ag.Spec.Replicas[i].AvailabilityMode),
			FailoverMode:     mapFailoverMode(ag.Spec.Replicas[i].FailoverMode),
			SeedingMode:      mapSeedingMode(ag.Spec.Replicas[i].SeedingMode),
			SecondaryRole:    mapSecondaryRole(ag.Spec.Replicas[i].SecondaryRole),
		})
	}

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	return primaryClient.CreateAG(sqlCtx, &config)
}

func (r *AvailabilityGroupReconciler) joinSecondaries(ctx context.Context, ag *v1alpha1.AvailabilityGroup) error {
	// Skip the first replica (primary), join all others
	for i := 1; i < len(ag.Spec.Replicas); i++ {
		replica := ag.Spec.Replicas[i]
		username, password, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, replica.Server.CredentialsSecret.Name)
		if err != nil {
			return fmt.Errorf("failed to get credentials for replica %s: %w", replica.ServerName, err)
		}

		secondaryClient, err := connectToSQL(replica.Server, username, password, r.SQLClientFactory)
		if err != nil {
			return fmt.Errorf("failed to connect to replica %s: %w", replica.ServerName, err)
		}

		// Ensure HADR endpoint on secondary
		sqlCtx, cancel := sqlContext(ctx)
		endpointExists, err := secondaryClient.HADREndpointExists(sqlCtx)
		cancel()
		if err != nil {
			secondaryClient.Close()
			return fmt.Errorf("failed to check HADR endpoint on %s: %w", replica.ServerName, err)
		}
		if !endpointExists {
			sqlCtx2, cancel2 := sqlContext(ctx)
			err = secondaryClient.CreateHADREndpoint(sqlCtx2, 5022)
			cancel2()
			if err != nil {
				secondaryClient.Close()
				return fmt.Errorf("failed to create HADR endpoint on %s: %w", replica.ServerName, err)
			}
		}

		// Join the AG
		clusterType := "EXTERNAL"
		if ag.Spec.ClusterType != nil {
			clusterType = mapClusterType(*ag.Spec.ClusterType)
		}
		sqlCtx3, cancel3 := sqlContext(ctx)
		err = secondaryClient.JoinAG(sqlCtx3, ag.Spec.AGName, clusterType)
		cancel3()
		if err != nil {
			secondaryClient.Close()
			return fmt.Errorf("failed to join AG on replica %s: %w", replica.ServerName, err)
		}

		// Grant CREATE ANY DATABASE for automatic seeding
		if replica.SeedingMode == v1alpha1.SeedingModeAutomatic {
			sqlCtx4, cancel4 := sqlContext(ctx)
			err = secondaryClient.GrantAGCreateDatabase(sqlCtx4, ag.Spec.AGName)
			cancel4()
			if err != nil {
				secondaryClient.Close()
				return fmt.Errorf("failed to grant create database on replica %s: %w", replica.ServerName, err)
			}
		}

		secondaryClient.Close()
		r.Recorder.Event(ag, corev1.EventTypeNormal, "ReplicaJoined",
			fmt.Sprintf("Replica %s joined AG %s", replica.ServerName, ag.Spec.AGName))
	}
	return nil
}

func (r *AvailabilityGroupReconciler) reconcileDatabases(ctx context.Context, ag *v1alpha1.AvailabilityGroup, primaryClient sqlclient.SQLClient) error {
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	agStatus, err := primaryClient.GetAGStatus(sqlCtx, ag.Spec.AGName)
	if err != nil {
		return err
	}

	// Build sets of desired and current databases
	desired := make(map[string]bool)
	for _, db := range ag.Spec.Databases {
		desired[db.Name] = true
	}
	current := make(map[string]bool)
	for _, db := range agStatus.Databases {
		current[db.Name] = true
	}

	// Add missing databases
	for dbName := range desired {
		if current[dbName] {
			continue
		}
		sqlCtx2, cancel2 := sqlContext(ctx)
		err := primaryClient.AddDatabaseToAG(sqlCtx2, ag.Spec.AGName, dbName)
		cancel2()
		if err != nil {
			return fmt.Errorf("failed to add database %s to AG: %w", dbName, err)
		}
		r.Recorder.Event(ag, corev1.EventTypeNormal, "DatabaseAddedToAG",
			fmt.Sprintf("Database %s added to AG %s", dbName, ag.Spec.AGName))
	}

	// Remove databases no longer in spec
	for dbName := range current {
		if desired[dbName] {
			continue
		}
		sqlCtx3, cancel3 := sqlContext(ctx)
		err := primaryClient.RemoveDatabaseFromAG(sqlCtx3, ag.Spec.AGName, dbName)
		cancel3()
		if err != nil {
			return fmt.Errorf("failed to remove database %s from AG: %w", dbName, err)
		}
		r.Recorder.Event(ag, corev1.EventTypeNormal, "DatabaseRemovedFromAG",
			fmt.Sprintf("Database %s removed from AG %s", dbName, ag.Spec.AGName))
	}

	return nil
}

func (r *AvailabilityGroupReconciler) reconcileListener(ctx context.Context, ag *v1alpha1.AvailabilityGroup, primaryClient sqlclient.SQLClient) error {
	if ag.Spec.Listener == nil {
		return nil
	}

	// Check if AG already has a listener by querying status
	// For simplicity, we attempt to add and handle "already exists" errors
	listenerConfig := sqlclient.AGListenerConfig{
		Name: ag.Spec.Listener.Name,
		Port: 1433,
	}
	if ag.Spec.Listener.Port != nil {
		listenerConfig.Port = int(*ag.Spec.Listener.Port)
	}
	for _, ip := range ag.Spec.Listener.IPAddresses {
		listenerConfig.IPAddresses = append(listenerConfig.IPAddresses, sqlclient.AGListenerIPConfig{
			IP:         ip.IP,
			SubnetMask: ip.SubnetMask,
		})
	}

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	err := primaryClient.AddListenerToAG(sqlCtx, ag.Spec.AGName, listenerConfig)
	if err != nil {
		// Listener may already exist — log but don't fail
		log.FromContext(ctx).V(1).Info("listener add returned error (may already exist)", "error", err)
	} else {
		r.Recorder.Event(ag, corev1.EventTypeNormal, "ListenerCreated",
			fmt.Sprintf("Listener %s created for AG %s", ag.Spec.Listener.Name, ag.Spec.AGName))
	}

	return nil
}

func (r *AvailabilityGroupReconciler) updateAGStatus(ctx context.Context, ag *v1alpha1.AvailabilityGroup, agStatus *sqlclient.AGStatus) error {
	patch := client.MergeFrom(ag.DeepCopy())

	ag.Status.PrimaryReplica = agStatus.PrimaryReplica
	ag.Status.ObservedGeneration = ag.Generation

	// Map replica states
	ag.Status.Replicas = make([]v1alpha1.AGReplicaStatus, len(agStatus.Replicas))
	for i, rs := range agStatus.Replicas {
		ag.Status.Replicas[i] = v1alpha1.AGReplicaStatus{
			ServerName:           rs.ServerName,
			Role:                 rs.Role,
			SynchronizationState: rs.SynchronizationState,
			Connected:            rs.Connected,
		}
		synced := float64(0)
		if rs.SynchronizationState == "SYNCHRONIZED" || rs.SynchronizationState == "SYNCHRONIZING" {
			synced = 1
		}
		opmetrics.AGReplicaLag.WithLabelValues(ag.Spec.AGName, ag.Namespace, rs.ServerName, rs.Role).Set(synced)
	}

	// Map database states
	ag.Status.Databases = make([]v1alpha1.AGDatabaseStatus, len(agStatus.Databases))
	for i, ds := range agStatus.Databases {
		ag.Status.Databases[i] = v1alpha1.AGDatabaseStatus{
			Name:                 ds.Name,
			SynchronizationState: ds.SynchronizationState,
			Joined:               ds.Joined,
		}
	}

	// Determine readiness: all replicas connected and synchronized
	allHealthy := true
	for _, rs := range agStatus.Replicas {
		if !rs.Connected || (rs.SynchronizationState != "SYNCHRONIZED" && rs.SynchronizationState != "SYNCHRONIZING") {
			allHealthy = false
			break
		}
	}

	condStatus := metav1.ConditionTrue
	reason := v1alpha1.ReasonReady
	message := fmt.Sprintf("AG %s is healthy with primary %s", ag.Spec.AGName, agStatus.PrimaryReplica)
	if !allHealthy {
		condStatus = metav1.ConditionFalse
		reason = "ReplicaNotHealthy"
		message = fmt.Sprintf("AG %s has unhealthy replicas", ag.Spec.AGName)
	}

	meta.SetStatusCondition(&ag.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ag.Generation,
	})

	return r.Status().Patch(ctx, ag, patch)
}

func (r *AvailabilityGroupReconciler) handleDeletion(ctx context.Context, ag *v1alpha1.AvailabilityGroup) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(ag, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	// Drop the AG on the primary (use known primary, fallback to first replica)
	primaryReplica := ag.Spec.Replicas[0]
	if ag.Status.PrimaryReplica != "" {
		for i := range ag.Spec.Replicas {
			if ag.Spec.Replicas[i].ServerName == ag.Status.PrimaryReplica {
				primaryReplica = ag.Spec.Replicas[i]
				break
			}
		}
	}
	username, password, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, primaryReplica.Server.CredentialsSecret.Name)
	if err != nil {
		logger.Error(err, "failed to get credentials for cleanup, removing finalizer anyway")
	} else {
		primaryClient, err := connectToSQL(primaryReplica.Server, username, password, r.SQLClientFactory)
		if err != nil {
			logger.Error(err, "failed to connect for cleanup, removing finalizer anyway")
		} else {
			defer primaryClient.Close()
			sqlCtx, cancel := sqlContext(ctx)
			defer cancel()
			if err := primaryClient.DropAG(sqlCtx, ag.Spec.AGName); err != nil {
				logger.Error(err, "failed to drop AG, removing finalizer anyway")
			} else {
				r.Recorder.Event(ag, corev1.EventTypeNormal, "AvailabilityGroupDropped",
					fmt.Sprintf("Availability Group %s dropped", ag.Spec.AGName))
				logger.Info("availability group dropped", "ag", ag.Spec.AGName)
			}
		}
	}

	controllerutil.RemoveFinalizer(ag, v1alpha1.Finalizer)
	if err := r.Update(ctx, ag); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

//nolint:unparam // returns (Result, error) for consistent controller pattern
func (r *AvailabilityGroupReconciler) setConditionAndReturn(ctx context.Context, ag *v1alpha1.AvailabilityGroup,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(ag.DeepCopy())
	meta.SetStatusCondition(&ag.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ag.Generation,
	})
	ag.Status.ObservedGeneration = ag.Generation

	if err := r.Status().Patch(ctx, ag, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AvailabilityGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AvailabilityGroup{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToAGs(mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 2,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](2*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(5), 50)},
			),
		}).
		Complete(r)
}

// --- Helpers ---

func mapAvailabilityMode(mode v1alpha1.AvailabilityMode) string {
	switch mode {
	case v1alpha1.AvailabilityModeAsynchronous:
		return "ASYNCHRONOUS_COMMIT"
	default:
		return "SYNCHRONOUS_COMMIT"
	}
}

func mapFailoverMode(mode v1alpha1.FailoverMode) string {
	switch mode {
	case v1alpha1.FailoverModeManual:
		return "MANUAL"
	default:
		return "AUTOMATIC"
	}
}

func mapSeedingMode(mode v1alpha1.SeedingMode) string {
	switch mode {
	case v1alpha1.SeedingModeManual:
		return "MANUAL"
	default:
		return "AUTOMATIC"
	}
}

func mapSecondaryRole(role v1alpha1.SecondaryRole) string {
	switch role {
	case v1alpha1.SecondaryRoleAllowAll:
		return "ALL"
	case v1alpha1.SecondaryRoleReadIntentOnly:
		return "READ_ONLY"
	default:
		return "NO"
	}
}

// handleAutoFailover attempts automatic failover when the primary is unreachable.
func (r *AvailabilityGroupReconciler) handleAutoFailover(ctx context.Context, ag *v1alpha1.AvailabilityGroup, requeue time.Duration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check cooldown
	cooldown := 60 * time.Second
	if ag.Spec.FailoverCooldown != nil {
		if d, err := time.ParseDuration(*ag.Spec.FailoverCooldown); err == nil {
			cooldown = d
		}
	}
	if ag.Status.LastAutoFailoverTime != nil {
		elapsed := time.Since(ag.Status.LastAutoFailoverTime.Time)
		if elapsed < cooldown {
			logger.Info("auto-failover cooldown active", "remaining", cooldown-elapsed)
			return ctrl.Result{RequeueAfter: cooldown - elapsed}, nil
		}
	}

	// Acquire Lease to prevent split-brain
	leaseName := fmt.Sprintf("ag-failover-%s", ag.Spec.AGName)
	if !r.tryAcquireLease(ctx, ag.Namespace, leaseName) {
		logger.Info("another operator instance holds the failover lease")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Try each replica (except the known primary) to find one we can connect to
	knownPrimary := ag.Status.PrimaryReplica
	if knownPrimary == "" {
		knownPrimary = ag.Spec.Replicas[0].ServerName
	}
	for i := 0; i < len(ag.Spec.Replicas); i++ {
		if ag.Spec.Replicas[i].ServerName == knownPrimary {
			continue
		}
		replica := ag.Spec.Replicas[i]
		u, p, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, replica.Server.CredentialsSecret.Name)
		if err != nil {
			continue
		}

		secondaryClient, err := connectToSQL(replica.Server, u, p, r.SQLClientFactory)
		if err != nil {
			continue
		}

		// Check if this replica is already PRIMARY (failover already happened externally)
		sqlCtx, cancel := sqlContext(ctx)
		role, err := secondaryClient.GetAGReplicaRole(sqlCtx, ag.Spec.AGName, replica.ServerName)
		cancel()
		if err != nil {
			secondaryClient.Close()
			continue
		}

		if role == "PRIMARY" {
			// Already failed over externally, just update status
			logger.Info("replica is already primary, updating status", "replica", replica.ServerName)
			sqlCtx2, cancel2 := sqlContext(ctx)
			agStatus, err := secondaryClient.GetAGStatus(sqlCtx2, ag.Spec.AGName)
			cancel2()
			secondaryClient.Close()
			if err == nil {
				_ = r.updateAGStatus(ctx, ag, agStatus)
			}
			return ctrl.Result{RequeueAfter: requeueWithJitter(requeue)}, nil
		}

		// Execute failover on this secondary
		logger.Info("executing auto-failover", "target", replica.ServerName, "ag", ag.Spec.AGName)

		sqlCtx3, cancel3 := sqlContext(ctx)
		failoverErr := secondaryClient.ForceFailoverAG(sqlCtx3, ag.Spec.AGName)
		cancel3()
		secondaryClient.Close()

		if failoverErr != nil {
			logger.Error(failoverErr, "auto-failover failed", "target", replica.ServerName)
			r.Recorder.Event(ag, corev1.EventTypeWarning, "AutoFailoverFailed",
				fmt.Sprintf("Auto-failover to %s failed: %v", replica.ServerName, failoverErr))
			continue
		}

		// Success
		now := metav1.Now()
		patch := client.MergeFrom(ag.DeepCopy())
		ag.Status.PrimaryReplica = replica.ServerName
		ag.Status.LastAutoFailoverTime = &now
		ag.Status.AutoFailoverCount++
		meta.SetStatusCondition(&ag.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             "AutoFailoverExecuted",
			Message:            fmt.Sprintf("Auto-failover: new primary is %s", replica.ServerName),
			ObservedGeneration: ag.Generation,
		})
		_ = r.Status().Patch(ctx, ag, patch)

		r.Recorder.Event(ag, corev1.EventTypeNormal, "AutoFailoverCompleted",
			fmt.Sprintf("AG %s auto-failover to %s completed", ag.Spec.AGName, replica.ServerName))
		logger.Info("auto-failover completed", "newPrimary", replica.ServerName)

		// Update pod role labels immediately for fast service routing convergence
		r.updateReplicaRoleLabelsFromAG(ctx, ag, replica.ServerName)

		opmetrics.ReconcileTotal.WithLabelValues("AvailabilityGroup", "auto_failover").Inc()
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	logger.Info("no reachable secondary for auto-failover")
	r.Recorder.Event(ag, corev1.EventTypeWarning, "AutoFailoverNoCandidate",
		"Primary unreachable and no secondary available for failover")
	return ctrl.Result{}, fmt.Errorf("no failover candidates available")
}

// tryAcquireLease attempts to acquire a Kubernetes Lease for split-brain prevention.
func (r *AvailabilityGroupReconciler) tryAcquireLease(ctx context.Context, namespace, leaseName string) bool {
	now := metav1.NewMicroTime(time.Now())
	leaseDuration := int32(30)
	holder := fmt.Sprintf("%s/%d", os.Getenv("HOSTNAME"), os.Getpid())

	lease := &coordinationv1.Lease{}
	err := r.Get(ctx, types.NamespacedName{Name: leaseName, Namespace: namespace}, lease)

	if apierrors.IsNotFound(err) {
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: leaseName, Namespace: namespace},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &leaseDuration,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		return r.Create(ctx, lease) == nil
	}
	if err != nil {
		return false
	}

	// We hold it → renew
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == holder {
		patch := client.MergeFrom(lease.DeepCopy())
		lease.Spec.RenewTime = &now
		return r.Patch(ctx, lease, patch) == nil
	}

	// Expired → take over
	if lease.Spec.RenewTime != nil && lease.Spec.LeaseDurationSeconds != nil {
		expiry := lease.Spec.RenewTime.Time.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
		if time.Now().After(expiry) {
			patch := client.MergeFrom(lease.DeepCopy())
			lease.Spec.HolderIdentity = &holder
			lease.Spec.AcquireTime = &now
			lease.Spec.RenewTime = &now
			return r.Patch(ctx, lease, patch) == nil
		}
	}

	return false
}

// updateReplicaRoleLabelsFromAG labels pods with primary/secondary based on the AG replica list.
// It uses the AG spec replicas to determine which pods to label.
func (r *AvailabilityGroupReconciler) updateReplicaRoleLabelsFromAG(ctx context.Context, ag *v1alpha1.AvailabilityGroup, primaryServerName string) {
	logger := log.FromContext(ctx)

	for _, replica := range ag.Spec.Replicas {
		// The ServerName is typically a short pod name like "sql-0"
		podName := replica.ServerName

		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: ag.Namespace}, &pod); err != nil {
			continue
		}

		desiredRole := RoleSecondary
		if podName == primaryServerName {
			desiredRole = RolePrimary
		}

		if pod.Labels[LabelRole] == desiredRole {
			continue
		}

		patch := client.MergeFrom(pod.DeepCopy())
		if pod.Labels == nil {
			pod.Labels = make(map[string]string)
		}
		pod.Labels[LabelRole] = desiredRole
		if err := r.Patch(ctx, &pod, patch); err != nil {
			logger.Error(err, "failed to update pod role label", "pod", podName, "role", desiredRole)
		}
	}
}

func mapClusterType(ct string) string {
	switch ct {
	case "WSFC":
		return "WSFC"
	case "None":
		return "NONE"
	default:
		return "EXTERNAL"
	}
}

func mapBackupPreference(pref string) string {
	switch pref {
	case "Primary":
		return "PRIMARY"
	case "SecondaryOnly":
		return "SECONDARY_ONLY"
	case "None":
		return "NONE"
	default:
		return "SECONDARY"
	}
}
