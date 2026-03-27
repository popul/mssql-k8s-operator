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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// LoginReconciler reconciles a Login object.
type LoginReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *LoginReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()
	defer func() {
		opmetrics.ReconcileDuration.WithLabelValues("Login").Observe(time.Since(start).Seconds())
	}()

	// 1. Fetch
	var login v1alpha1.Login
	if err := r.Get(ctx, req.NamespacedName, &login); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer
	if login.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &login)
	}

	if !controllerutil.ContainsFinalizer(&login, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&login, v1alpha1.Finalizer)
		if err := r.Update(ctx, &login); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Read credentials Secret
	username, saPassword, err := getCredentialsFromSecret(ctx, r.Client, login.Namespace, login.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", login.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Read password Secret
	var pwSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: login.Spec.PasswordSecret.Name, Namespace: login.Namespace}, &pwSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Password secret %q not found", login.Spec.PasswordSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonConnectionFailed,
			fmt.Sprintf("Failed to fetch password secret: %v", err))
	}
	loginPassword, ok := pwSecret.Data["password"]
	if !ok {
		return r.setConditionAndReturn(ctx, &login, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret,
			fmt.Sprintf("Password secret %q missing 'password' key", login.Spec.PasswordSecret.Name))
	}

	// 5. Connect
	sqlClient, err := connectToSQL(login.Spec.Server, username, saPassword, r.SQLClientFactory)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&login, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 6. Observe
	exists, err := sqlClient.LoginExists(ctx, login.Spec.LoginName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check login existence: %w", err)
	}

	// 7. Compare & Act
	if !exists {
		if err := sqlClient.CreateLogin(ctx, login.Spec.LoginName, string(loginPassword)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create login %s: %w", login.Spec.LoginName, err)
		}
		r.Recorder.Event(&login, corev1.EventTypeNormal, "LoginCreated",
			fmt.Sprintf("Login %s created", login.Spec.LoginName))
		logger.Info("login created", "login", login.Spec.LoginName)
	} else {
		// Check password rotation
		if pwSecret.ResourceVersion != login.Status.PasswordSecretResourceVersion {
			if err := sqlClient.UpdateLoginPassword(ctx, login.Spec.LoginName, string(loginPassword)); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update login password: %w", err)
			}
			r.Recorder.Event(&login, corev1.EventTypeNormal, "LoginPasswordRotated",
				fmt.Sprintf("Login %s password rotated", login.Spec.LoginName))
			logger.Info("login password rotated", "login", login.Spec.LoginName)
		}
	}

	// Default database
	if login.Spec.DefaultDatabase != nil {
		currentDB, err := sqlClient.GetLoginDefaultDatabase(ctx, login.Spec.LoginName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get default database: %w", err)
		}
		if currentDB != *login.Spec.DefaultDatabase {
			if err := sqlClient.SetLoginDefaultDatabase(ctx, login.Spec.LoginName, *login.Spec.DefaultDatabase); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set default database: %w", err)
			}
		}
	}

	// Server roles: compute diff
	if err := r.reconcileServerRoles(ctx, &login, sqlClient, login.Spec.LoginName, login.Spec.ServerRoles); err != nil {
		opmetrics.ReconcileErrors.WithLabelValues("Login", "ServerRoleReconciliation").Inc()
		return ctrl.Result{}, err
	}

	// 8. Status
	login.Status.PasswordSecretResourceVersion = pwSecret.ResourceVersion
	opmetrics.ReconcileTotal.WithLabelValues("Login", "success").Inc()
	return r.setConditionAndReturn(ctx, &login, metav1.ConditionTrue, v1alpha1.ReasonReady,
		fmt.Sprintf("Login %s is ready", login.Spec.LoginName))
}

func (r *LoginReconciler) reconcileServerRoles(ctx context.Context, login *v1alpha1.Login, sqlClient sqlclient.SQLClient, loginName string, desiredRoles []string) error {
	currentRoles, err := sqlClient.GetLoginServerRoles(ctx, loginName)
	if err != nil {
		return fmt.Errorf("failed to get server roles: %w", err)
	}

	currentSet := toSet(currentRoles)
	desiredSet := toSet(desiredRoles)

	// Add missing roles
	for role := range desiredSet {
		if !currentSet[role] {
			if err := sqlClient.AddLoginToServerRole(ctx, loginName, role); err != nil {
				return fmt.Errorf("failed to add role %s: %w", role, err)
			}
			r.Recorder.Event(login, corev1.EventTypeNormal, "ServerRoleAdded",
				fmt.Sprintf("Login %s added to server role %s", loginName, role))
		}
	}

	// Remove extra roles
	for role := range currentSet {
		if !desiredSet[role] {
			if err := sqlClient.RemoveLoginFromServerRole(ctx, loginName, role); err != nil {
				return fmt.Errorf("failed to remove role %s: %w", role, err)
			}
			r.Recorder.Event(login, corev1.EventTypeNormal, "ServerRoleRemoved",
				fmt.Sprintf("Login %s removed from server role %s", loginName, role))
		}
	}

	return nil
}

func (r *LoginReconciler) handleDeletion(ctx context.Context, login *v1alpha1.Login) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(login, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	policy := v1alpha1.DeletionPolicyRetain
	if login.Spec.DeletionPolicy != nil {
		policy = *login.Spec.DeletionPolicy
	}

	if policy == v1alpha1.DeletionPolicyDelete {
		username, password, err := getCredentialsFromSecret(ctx, r.Client, login.Namespace, login.Spec.Server.CredentialsSecret.Name)
		if err != nil {
			logger.Error(err, "failed to get credentials for cleanup")
		} else {
			sqlClient, err := connectToSQL(login.Spec.Server, username, password, r.SQLClientFactory)
			if err != nil {
				logger.Error(err, "failed to connect for cleanup")
			} else {
				defer sqlClient.Close()

				// Check if login has active users
				hasUsers, err := sqlClient.LoginHasUsers(ctx, login.Spec.LoginName)
				if err != nil {
					logger.Error(err, "failed to check if login has users")
				} else if hasUsers {
					r.Recorder.Event(login, corev1.EventTypeWarning, v1alpha1.ReasonLoginInUse,
						fmt.Sprintf("Login %s is still in use by database users", login.Spec.LoginName))
					return r.setConditionAndReturn(ctx, login, metav1.ConditionFalse, v1alpha1.ReasonLoginInUse,
						"Login is still in use by database users, delete DatabaseUser CRs first")
				}

				if err := sqlClient.DropLogin(ctx, login.Spec.LoginName); err != nil {
					logger.Error(err, "failed to drop login")
				} else {
					r.Recorder.Event(login, corev1.EventTypeNormal, "LoginDropped",
						fmt.Sprintf("Login %s dropped", login.Spec.LoginName))
				}
			}
		}
	}

	controllerutil.RemoveFinalizer(login, v1alpha1.Finalizer)
	if err := r.Update(ctx, login); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *LoginReconciler) setConditionAndReturn(ctx context.Context, login *v1alpha1.Login,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	meta.SetStatusCondition(&login.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: login.Generation,
	})
	login.Status.ObservedGeneration = login.Generation

	if err := r.Status().Update(ctx, login); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (r *LoginReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Login{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}
