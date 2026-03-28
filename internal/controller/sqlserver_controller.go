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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"golang.org/x/time/rate"

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

func (r *SQLServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
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

	// 2. Only SqlLogin is supported for now (AzureAD/ManagedIdentity will need additional factories)
	if srv.Spec.AuthMethod != v1alpha1.AuthSqlLogin && srv.Spec.AuthMethod != "" {
		return r.setConditionAndReturn(ctx, &srv, metav1.ConditionFalse, "UnsupportedAuthMethod",
			fmt.Sprintf("Authentication method %s is not yet supported", srv.Spec.AuthMethod))
	}

	// 3. Read the credentials Secret
	secretNS := srv.Namespace
	if srv.Spec.CredentialsSecret != nil && srv.Spec.CredentialsSecret.Namespace != nil {
		secretNS = *srv.Spec.CredentialsSecret.Namespace
	}

	if srv.Spec.CredentialsSecret == nil {
		return r.setConditionAndReturn(ctx, &srv, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
			"credentialsSecret is required for SqlLogin auth")
	}

	username, password, err := getCredentialsFromSecret(ctx, r.Client, secretNS, srv.Spec.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &srv, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found in namespace %s", srv.Spec.CredentialsSecret.Name, secretNS))
		}
		return r.setConditionAndReturn(ctx, &srv, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect and probe
	port := int32(1433)
	if srv.Spec.Port != nil {
		port = *srv.Spec.Port
	}
	tlsEnabled := srv.Spec.TLS != nil && *srv.Spec.TLS

	sqlConn, err := r.SQLClientFactory(srv.Spec.Host, int(port), username, password, tlsEnabled)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&srv, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		opmetrics.SQLServerConnected.WithLabelValues(srv.Name, srv.Namespace, srv.Spec.Host).Set(0)
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlConn.Close()

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	if err := sqlConn.Ping(sqlCtx); err != nil {
		r.Recorder.Event(&srv, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to ping SQL Server: %w", err)
	}

	// 5. Gather server info
	version, _ := sqlConn.GetServerVersion(sqlCtx)
	edition, _ := sqlConn.GetServerEdition(sqlCtx)

	// 6. Update status
	now := metav1.Now()
	patch := client.MergeFrom(srv.DeepCopy())
	srv.Status.ServerVersion = version
	srv.Status.Edition = edition
	srv.Status.LastConnectedTime = &now
	srv.Status.ObservedGeneration = srv.Generation

	meta.SetStatusCondition(&srv.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1alpha1.ReasonReady,
		Message:            fmt.Sprintf("Connected to %s (%s)", srv.Spec.Host, version),
		ObservedGeneration: srv.Generation,
	})

	if err := r.Status().Patch(ctx, &srv, patch); err != nil {
		return ctrl.Result{}, err
	}

	opmetrics.ReconcileTotal.WithLabelValues("SQLServer", "success").Inc()
	opmetrics.SQLServerConnected.WithLabelValues(srv.Name, srv.Namespace, srv.Spec.Host).Set(1)
	return ctrl.Result{RequeueAfter: requeueWithJitter(60 * time.Second)}, nil
}

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

func mapSecretToSQLServers(ctx context.Context, c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.SQLServerList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, srv := range list.Items {
			if srv.Spec.CredentialsSecret != nil && srv.Spec.CredentialsSecret.Name == obj.GetName() {
				ns := srv.Namespace
				if srv.Spec.CredentialsSecret.Namespace != nil {
					ns = *srv.Spec.CredentialsSecret.Namespace
				}
				if ns == obj.GetNamespace() {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{Name: srv.Name, Namespace: srv.Namespace},
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
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToSQLServers(context.Background(), mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 3,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
