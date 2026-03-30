package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/time/rate"
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

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// PermissionReconciler reconciles a Permission object.
type PermissionReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=permissions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=permissions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=permissions/finalizers,verbs=update

func (r *PermissionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("Permission").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch
	var perm v1alpha1.Permission
	if err := r.Get(ctx, req.NamespacedName, &perm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer
	if perm.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &perm)
	}

	if !controllerutil.ContainsFinalizer(&perm, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&perm, v1alpha1.Finalizer)
		if err := r.Update(ctx, &perm); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Read credentials
	username, password, err := getCredentialsFromSecret(ctx, r.Client, perm.Namespace, perm.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &perm, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", perm.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &perm, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect
	sqlClient, err := connectToSQL(perm.Spec.Server, username, password, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&perm, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 5. Observe current permissions
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	currentPerms, err := sqlClient.GetPermissions(sqlCtx, perm.Spec.DatabaseName, perm.Spec.UserName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get current permissions: %w", err)
	}

	// 6. Build desired state
	desiredGrants := make(map[string]string) // key: "PERM|TARGET" -> "GRANT"
	desiredDenies := make(map[string]string)
	for _, g := range perm.Spec.Grants {
		key := permissionKey(g.Permission, g.On)
		desiredGrants[key] = g.On
	}
	for _, d := range perm.Spec.Denies {
		key := permissionKey(d.Permission, d.On)
		desiredDenies[key] = d.On
	}

	currentSet := make(map[string]sqlclient.PermissionState)
	for _, p := range currentPerms {
		key := permissionKey(p.Permission, p.Target)
		currentSet[key] = p
	}

	// 7. Apply grants
	for _, g := range perm.Spec.Grants {
		key := permissionKey(g.Permission, g.On)
		if cur, ok := currentSet[key]; ok && cur.State == "GRANT" {
			continue // already granted
		}
		sqlCtx2, cancel2 := sqlContext(ctx)
		err := sqlClient.GrantPermission(sqlCtx2, perm.Spec.DatabaseName, g.Permission, g.On, perm.Spec.UserName)
		cancel2()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to grant %s on %s: %w", g.Permission, g.On, err)
		}
		r.Recorder.Event(&perm, corev1.EventTypeNormal, "PermissionGranted",
			fmt.Sprintf("GRANT %s ON %s TO %s", g.Permission, g.On, perm.Spec.UserName))
	}

	// 8. Apply denies
	for _, d := range perm.Spec.Denies {
		key := permissionKey(d.Permission, d.On)
		if cur, ok := currentSet[key]; ok && cur.State == "DENY" {
			continue // already denied
		}
		sqlCtx3, cancel3 := sqlContext(ctx)
		err := sqlClient.DenyPermission(sqlCtx3, perm.Spec.DatabaseName, d.Permission, d.On, perm.Spec.UserName)
		cancel3()
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to deny %s on %s: %w", d.Permission, d.On, err)
		}
		r.Recorder.Event(&perm, corev1.EventTypeNormal, "PermissionDenied",
			fmt.Sprintf("DENY %s ON %s TO %s", d.Permission, d.On, perm.Spec.UserName))
	}

	// 9. Revoke permissions no longer desired
	for key, cur := range currentSet {
		if cur.State == "GRANT" {
			if _, desired := desiredGrants[key]; !desired {
				sqlCtx4, cancel4 := sqlContext(ctx)
				err := sqlClient.RevokePermission(sqlCtx4, perm.Spec.DatabaseName, cur.Permission, cur.Target, perm.Spec.UserName)
				cancel4()
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to revoke %s on %s: %w", cur.Permission, cur.Target, err)
				}
				r.Recorder.Event(&perm, corev1.EventTypeNormal, "PermissionRevoked",
					fmt.Sprintf("REVOKE %s ON %s FROM %s", cur.Permission, cur.Target, perm.Spec.UserName))
			}
		}
		if cur.State == "DENY" {
			if _, desired := desiredDenies[key]; !desired {
				sqlCtx5, cancel5 := sqlContext(ctx)
				err := sqlClient.RevokePermission(sqlCtx5, perm.Spec.DatabaseName, cur.Permission, cur.Target, perm.Spec.UserName)
				cancel5()
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to revoke deny %s on %s: %w", cur.Permission, cur.Target, err)
				}
				r.Recorder.Event(&perm, corev1.EventTypeNormal, "PermissionRevoked",
					fmt.Sprintf("REVOKE %s ON %s FROM %s (was DENY)", cur.Permission, cur.Target, perm.Spec.UserName))
			}
		}
	}

	// 10. Status
	opmetrics.ReconcileTotal.WithLabelValues("Permission", "success").Inc()
	return r.setConditionAndReturn(ctx, &perm, metav1.ConditionTrue, v1alpha1.ReasonReady,
		fmt.Sprintf("Permissions for user %s are reconciled", perm.Spec.UserName))
}

func permissionKey(permission, target string) string {
	return strings.ToUpper(permission) + "|" + strings.ToUpper(target)
}

func (r *PermissionReconciler) handleDeletion(ctx context.Context, perm *v1alpha1.Permission) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(perm, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	// Revoke all permissions on deletion
	username, password, err := getCredentialsFromSecret(ctx, r.Client, perm.Namespace, perm.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		logger.Error(err, "failed to get credentials for cleanup, removing finalizer anyway")
	} else {
		sqlClient, err := connectToSQL(perm.Spec.Server, username, password, r.SQLClientFactory)
		if err != nil {
			logger.Error(err, "failed to connect for cleanup, removing finalizer anyway")
		} else {
			defer sqlClient.Close()

			// Revoke all grants
			for _, g := range perm.Spec.Grants {
				sqlCtx, cancel := sqlContext(ctx)
				err := sqlClient.RevokePermission(sqlCtx, perm.Spec.DatabaseName, g.Permission, g.On, perm.Spec.UserName)
				cancel()
				if err != nil {
					logger.Error(err, "failed to revoke grant", "permission", g.Permission, "on", g.On)
				}
			}
			// Revoke all denies
			for _, d := range perm.Spec.Denies {
				sqlCtx, cancel := sqlContext(ctx)
				err := sqlClient.RevokePermission(sqlCtx, perm.Spec.DatabaseName, d.Permission, d.On, perm.Spec.UserName)
				cancel()
				if err != nil {
					logger.Error(err, "failed to revoke deny", "permission", d.Permission, "on", d.On)
				}
			}

			r.Recorder.Event(perm, corev1.EventTypeNormal, "PermissionsRevoked",
				fmt.Sprintf("All permissions revoked for user %s", perm.Spec.UserName))
		}
	}

	controllerutil.RemoveFinalizer(perm, v1alpha1.Finalizer)
	if err := r.Update(ctx, perm); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *PermissionReconciler) setConditionAndReturn(ctx context.Context, perm *v1alpha1.Permission,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	patch := client.MergeFrom(perm.DeepCopy())

	meta.SetStatusCondition(&perm.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: perm.Generation,
	})
	perm.Status.ObservedGeneration = perm.Generation

	if err := r.Status().Patch(ctx, perm, patch); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueWithJitter(requeueInterval)}, nil
	}
	return ctrl.Result{}, nil
}

func (r *PermissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Permission{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapSecretToPermissions(mgr.GetClient()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 5*time.Minute),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}
