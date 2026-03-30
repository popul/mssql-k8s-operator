package controller

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/time/rate"
	appsv1 "k8s.io/api/apps/v1"
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

// SQLServerReconciler reconciles a SQLServer object.
type SQLServerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=sqlservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=sqlservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=sqlservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

func (r *SQLServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("SQLServer").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch the SQLServer CR
	var srv v1alpha1.SQLServer
	if err := r.Get(ctx, req.NamespacedName, &srv); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Dispatch: managed vs external
	if srv.Spec.Instance != nil {
		return r.reconcileManaged(ctx, &srv)
	}
	return r.reconcileExternal(ctx, &srv)
}

// reconcileManaged handles managed SQL Server instances (StatefulSet + optional AG).
func (r *SQLServerReconciler) reconcileManaged(ctx context.Context, srv *v1alpha1.SQLServer) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	_ = logger
	patch := client.MergeFrom(srv.DeepCopy())

	// Finalizer
	if !controllerutil.ContainsFinalizer(srv, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(srv, v1alpha1.Finalizer)
		if err := r.Update(ctx, srv); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle deletion
	if srv.DeletionTimestamp != nil {
		logger.Info("handling managed SQLServer deletion")
		controllerutil.RemoveFinalizer(srv, v1alpha1.Finalizer)
		if err := r.Update(ctx, srv); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Phase 1: Infrastructure
	if err := r.reconcileConfigMap(ctx, srv); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile config: %w", err)
	}
	if err := r.reconcileHeadlessService(ctx, srv); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile headless service: %w", err)
	}
	if err := r.reconcileClientService(ctx, srv); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile client service: %w", err)
	}
	if err := r.reconcileStatefulSet(ctx, srv); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile statefulset: %w", err)
	}

	// Check StatefulSet readiness
	ready, readyReplicas, err := r.isStatefulSetReady(ctx, srv)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check statefulset readiness: %w", err)
	}

	srv.Status.ReadyReplicas = &readyReplicas
	srv.Status.Host = managedHost(srv)

	if !ready {
		meta.SetStatusCondition(&srv.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             v1alpha1.ReasonDeploymentProvisioning,
			Message:            fmt.Sprintf("StatefulSet not ready: %d/%d replicas", readyReplicas, *srv.Spec.Instance.Replicas),
			ObservedGeneration: srv.Generation,
		})
		srv.Status.ObservedGeneration = srv.Generation
		if err := r.Status().Patch(ctx, srv, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	replicas := int32(1)
	if srv.Spec.Instance.Replicas != nil {
		replicas = *srv.Spec.Instance.Replicas
	}

	// Phase 2: Certificates (cluster mode only)
	if replicas > 1 {
		certsReady, err := r.reconcileCertificates(ctx, srv)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to reconcile certificates: %w", err)
		}
		srv.Status.CertificatesReady = &certsReady
		if !certsReady {
			meta.SetStatusCondition(&srv.Status.Conditions, metav1.Condition{
				Type:               v1alpha1.ConditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             v1alpha1.ReasonCertificatesProvisioning,
				Message:            "Waiting for HADR certificates",
				ObservedGeneration: srv.Generation,
			})
			srv.Status.ObservedGeneration = srv.Generation
			if err := r.Status().Patch(ctx, srv, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		// Distribute certificates to SQL Server replicas
		if err := r.distributeCertificatesToSQL(ctx, srv); err != nil {
			logger.Error(err, "failed to distribute certificates to SQL replicas")
			// Non-fatal on first attempt, requeue
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// Phase 3: Availability Group (cluster mode only)
	if replicas > 1 {
		if err := r.reconcileManagedAG(ctx, srv); err != nil {
			logger.Error(err, "failed to reconcile managed AG")
			meta.SetStatusCondition(&srv.Status.Conditions, metav1.Condition{
				Type:               v1alpha1.ConditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             v1alpha1.ReasonAGProvisioning,
				Message:            fmt.Sprintf("AG provisioning: %s", err.Error()),
				ObservedGeneration: srv.Generation,
			})
			srv.Status.ObservedGeneration = srv.Generation
			if err := r.Status().Patch(ctx, srv, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// Phase 4: Persist PrimaryReplica in status if it was set during AG reconciliation
	if srv.Status.PrimaryReplica != "" {
		if err := r.Status().Patch(ctx, srv, patch); err != nil {
			return ctrl.Result{}, err
		}
		// Re-create patch from the now-updated object
		patch = client.MergeFrom(srv.DeepCopy())
	}

	// Phase 5: Probe SQL Server (connect to primary / standalone)
	probeHost := managedHost(srv)
	if replicas > 1 {
		probeHost = replicaHost(srv, 0) // Primary is always pod-0 initially
		if srv.Status.PrimaryReplica != "" {
			probeHost = srv.Status.PrimaryReplica
		}
	}

	return r.probeAndUpdateStatus(ctx, srv, patch, probeHost)
}

// reconcileExternal handles external SQL Server connections (existing behavior).
func (r *SQLServerReconciler) reconcileExternal(ctx context.Context, srv *v1alpha1.SQLServer) (ctrl.Result, error) {
	// Only SqlLogin is supported for now
	if srv.Spec.AuthMethod != v1alpha1.AuthSqlLogin && srv.Spec.AuthMethod != "" {
		return r.setConditionAndReturn(ctx, srv, metav1.ConditionFalse, "UnsupportedAuthMethod",
			fmt.Sprintf("Authentication method %s is not yet supported", srv.Spec.AuthMethod))
	}

	patch := client.MergeFrom(srv.DeepCopy())
	srv.Status.Host = srv.Spec.Host

	return r.probeAndUpdateStatus(ctx, srv, patch, srv.Spec.Host)
}

// probeAndUpdateStatus connects to SQL Server, gathers info, and updates status.
func (r *SQLServerReconciler) probeAndUpdateStatus(ctx context.Context, srv *v1alpha1.SQLServer, patch client.Patch, host string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Read the credentials Secret.
	// In managed mode, if credentialsSecret is not set, fall back to sa + saPasswordSecret.
	var username, password string
	var err error

	if srv.Spec.CredentialsSecret != nil {
		secretNS := srv.Namespace
		if srv.Spec.CredentialsSecret.Namespace != nil {
			secretNS = *srv.Spec.CredentialsSecret.Namespace
		}
		username, password, err = getCredentialsFromSecret(ctx, r.Client, secretNS, srv.Spec.CredentialsSecret.Name)
	} else if srv.Spec.Instance != nil {
		// Managed mode fallback: use sa + saPasswordSecret
		username, password, err = getCredentialsFromSAPasswordSecret(ctx, r.Client, srv.Namespace, srv.Spec.Instance.SAPasswordSecret.Name)
	} else {
		return r.setConditionAndReturn(ctx, srv, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
			"credentialsSecret is required for SqlLogin auth in external mode")
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, srv, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound, err.Error())
		}
		return r.setConditionAndReturn(ctx, srv, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// Connect and probe
	port := int32(1433)
	if srv.Spec.Port != nil {
		port = *srv.Spec.Port
	}
	tlsEnabled := srv.Spec.TLS != nil && *srv.Spec.TLS

	sqlConn, err := r.SQLClientFactory(host, int(port), username, password, tlsEnabled)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(srv, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		opmetrics.SQLServerConnected.WithLabelValues(srv.Name, srv.Namespace, host).Set(0)
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlConn.Close()

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	if err := sqlConn.Ping(sqlCtx); err != nil {
		r.Recorder.Event(srv, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to ping SQL Server: %w", err)
	}

	// Gather server info
	version, _ := sqlConn.GetServerVersion(sqlCtx)
	edition, _ := sqlConn.GetServerEdition(sqlCtx)

	// Update status
	now := metav1.Now()
	srv.Status.ServerVersion = version
	srv.Status.Edition = edition
	srv.Status.LastConnectedTime = &now
	srv.Status.ObservedGeneration = srv.Generation

	meta.SetStatusCondition(&srv.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1alpha1.ReasonReady,
		Message:            fmt.Sprintf("Connected to %s (%s)", host, version),
		ObservedGeneration: srv.Generation,
	})

	if err := r.Status().Patch(ctx, srv, patch); err != nil {
		return ctrl.Result{}, err
	}

	opmetrics.ReconcileTotal.WithLabelValues("SQLServer", "success").Inc()
	opmetrics.SQLServerConnected.WithLabelValues(srv.Name, srv.Namespace, host).Set(1)
	return ctrl.Result{RequeueAfter: requeueWithJitter(60 * time.Second)}, nil
}

//nolint:unparam // status kept for API consistency
func (r *SQLServerReconciler) setConditionAndReturn(ctx context.Context, srv *v1alpha1.SQLServer,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(srv.DeepCopy())
	meta.SetStatusCondition(&srv.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: srv.Generation,
	})
	srv.Status.ObservedGeneration = srv.Generation

	if err := r.Status().Patch(ctx, srv, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func mapSecretToSQLServers(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.SQLServerList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.CredentialsSecret != nil && list.Items[i].Spec.CredentialsSecret.Name == obj.GetName() {
				ns := list.Items[i].Namespace
				if list.Items[i].Spec.CredentialsSecret.Namespace != nil {
					ns = *list.Items[i].Spec.CredentialsSecret.Namespace
				}
				if ns == obj.GetNamespace() {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
					})
				}
			}
		}
		return requests
	}
}

func (r *SQLServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SQLServer{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToSQLServers(mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 3,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
