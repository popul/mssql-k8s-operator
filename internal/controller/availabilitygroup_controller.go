package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	"golang.org/x/time/rate"

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

	// 3. Connect to the primary replica (first replica in the spec)
	primaryReplica := ag.Spec.Replicas[0]
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
		return ctrl.Result{}, fmt.Errorf("failed to connect to primary replica: %w", err)
	}
	defer primaryClient.Close()

	// 4. Ensure HADR endpoint exists on primary
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

	opmetrics.ReconcileTotal.WithLabelValues("AvailabilityGroup", "success").Inc()
	return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
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

	for _, db := range ag.Spec.Databases {
		config.Databases = append(config.Databases, db.Name)
	}

	for _, replica := range ag.Spec.Replicas {
		config.Replicas = append(config.Replicas, sqlclient.AGReplicaConfig{
			ServerName:       replica.ServerName,
			EndpointURL:      replica.EndpointURL,
			AvailabilityMode: mapAvailabilityMode(replica.AvailabilityMode),
			FailoverMode:     mapFailoverMode(replica.FailoverMode),
			SeedingMode:      mapSeedingMode(replica.SeedingMode),
			SecondaryRole:    mapSecondaryRole(replica.SecondaryRole),
		})
	}

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	return primaryClient.CreateAG(sqlCtx, config)
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
		sqlCtx3, cancel3 := sqlContext(ctx)
		err = secondaryClient.JoinAG(sqlCtx3, ag.Spec.AGName)
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
		if !current[dbName] {
			sqlCtx2, cancel2 := sqlContext(ctx)
			err := primaryClient.AddDatabaseToAG(sqlCtx2, ag.Spec.AGName, dbName)
			cancel2()
			if err != nil {
				return fmt.Errorf("failed to add database %s to AG: %w", dbName, err)
			}
			r.Recorder.Event(ag, corev1.EventTypeNormal, "DatabaseAddedToAG",
				fmt.Sprintf("Database %s added to AG %s", dbName, ag.Spec.AGName))
		}
	}

	// Remove databases no longer in spec
	for dbName := range current {
		if !desired[dbName] {
			sqlCtx3, cancel3 := sqlContext(ctx)
			err := primaryClient.RemoveDatabaseFromAG(sqlCtx3, ag.Spec.AGName, dbName)
			cancel3()
			if err != nil {
				return fmt.Errorf("failed to remove database %s from AG: %w", dbName, err)
			}
			r.Recorder.Event(ag, corev1.EventTypeNormal, "DatabaseRemovedFromAG",
				fmt.Sprintf("Database %s removed from AG %s", dbName, ag.Spec.AGName))
		}
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

	// Drop the AG on the primary
	primaryReplica := ag.Spec.Replicas[0]
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
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToAGs(context.Background(), mgr.GetClient()))).
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
