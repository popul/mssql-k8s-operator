package controller

import (
	"context"
	"fmt"

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

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// DatabaseUserReconciler reconciles a DatabaseUser object.
type DatabaseUserReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=databaseusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databaseusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databaseusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=mssql.popul.io,resources=logins,verbs=get;list;watch

func (r *DatabaseUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch
	var dbUser v1alpha1.DatabaseUser
	if err := r.Get(ctx, req.NamespacedName, &dbUser); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer
	if dbUser.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &dbUser)
	}

	if !controllerutil.ContainsFinalizer(&dbUser, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&dbUser, v1alpha1.Finalizer)
		if err := r.Update(ctx, &dbUser); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Resolve LoginRef
	var login v1alpha1.Login
	if err := r.Get(ctx, types.NamespacedName{Name: dbUser.Spec.LoginRef.Name, Namespace: dbUser.Namespace}, &login); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionFalse, v1alpha1.ReasonLoginRefNotFound,
				fmt.Sprintf("Login CR %q not found", dbUser.Spec.LoginRef.Name))
		}
		return ctrl.Result{}, err
	}

	// 4. Read credentials Secret
	username, password, err := r.getCredentials(ctx, dbUser.Namespace, dbUser.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", dbUser.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 5. Connect
	port := int32(1433)
	if dbUser.Spec.Server.Port != nil {
		port = *dbUser.Spec.Server.Port
	}
	tlsEnabled := false
	if dbUser.Spec.Server.TLS != nil {
		tlsEnabled = *dbUser.Spec.Server.TLS
	}

	sqlClient, err := r.SQLClientFactory(dbUser.Spec.Server.Host, int(port), username, password, tlsEnabled)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&dbUser, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 6. Observe
	exists, err := sqlClient.UserExists(ctx, dbUser.Spec.DatabaseName, dbUser.Spec.UserName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check user existence: %w", err)
	}

	// 7. Compare & Act
	loginName := login.Spec.LoginName
	if !exists {
		if err := sqlClient.CreateUser(ctx, dbUser.Spec.DatabaseName, dbUser.Spec.UserName, loginName); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create user %s: %w", dbUser.Spec.UserName, err)
		}
		r.Recorder.Event(&dbUser, corev1.EventTypeNormal, "DatabaseUserCreated",
			fmt.Sprintf("User %s created in database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
		logger.Info("database user created", "user", dbUser.Spec.UserName, "database", dbUser.Spec.DatabaseName)
	}

	// Reconcile database roles
	if err := r.reconcileDatabaseRoles(ctx, sqlClient, dbUser.Spec.DatabaseName, dbUser.Spec.UserName, dbUser.Spec.DatabaseRoles); err != nil {
		return ctrl.Result{}, err
	}

	// 8. Status
	return r.setConditionAndReturn(ctx, &dbUser, metav1.ConditionTrue, v1alpha1.ReasonReady,
		fmt.Sprintf("User %s is ready in database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
}

func (r *DatabaseUserReconciler) reconcileDatabaseRoles(ctx context.Context, sqlClient sqlclient.SQLClient, dbName, userName string, desiredRoles []string) error {
	currentRoles, err := sqlClient.GetUserDatabaseRoles(ctx, dbName, userName)
	if err != nil {
		return fmt.Errorf("failed to get database roles: %w", err)
	}

	currentSet := toSet(currentRoles)
	desiredSet := toSet(desiredRoles)

	// Add missing roles
	for role := range desiredSet {
		if !currentSet[role] {
			if err := sqlClient.AddUserToDatabaseRole(ctx, dbName, userName, role); err != nil {
				return fmt.Errorf("failed to add role %s: %w", role, err)
			}
		}
	}

	// Remove extra roles
	for role := range currentSet {
		if !desiredSet[role] {
			if err := sqlClient.RemoveUserFromDatabaseRole(ctx, dbName, userName, role); err != nil {
				return fmt.Errorf("failed to remove role %s: %w", role, err)
			}
		}
	}

	return nil
}

func (r *DatabaseUserReconciler) handleDeletion(ctx context.Context, dbUser *v1alpha1.DatabaseUser) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(dbUser, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	username, password, err := r.getCredentials(ctx, dbUser.Namespace, dbUser.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		logger.Error(err, "failed to get credentials for cleanup")
	} else {
		port := int32(1433)
		if dbUser.Spec.Server.Port != nil {
			port = *dbUser.Spec.Server.Port
		}
		tlsEnabled := false
		if dbUser.Spec.Server.TLS != nil {
			tlsEnabled = *dbUser.Spec.Server.TLS
		}
		sqlClient, err := r.SQLClientFactory(dbUser.Spec.Server.Host, int(port), username, password, tlsEnabled)
		if err != nil {
			logger.Error(err, "failed to connect for cleanup")
		} else {
			defer sqlClient.Close()

			// Check if user owns objects
			owns, err := sqlClient.UserOwnsObjects(ctx, dbUser.Spec.DatabaseName, dbUser.Spec.UserName)
			if err != nil {
				logger.Error(err, "failed to check if user owns objects")
			} else if owns {
				r.Recorder.Event(dbUser, corev1.EventTypeWarning, v1alpha1.ReasonUserOwnsObjects,
					fmt.Sprintf("User %s owns objects in database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
				return r.setConditionAndReturn(ctx, dbUser, metav1.ConditionFalse, v1alpha1.ReasonUserOwnsObjects,
					"User owns objects in the database, transfer ownership first")
			}

			if err := sqlClient.DropUser(ctx, dbUser.Spec.DatabaseName, dbUser.Spec.UserName); err != nil {
				logger.Error(err, "failed to drop user")
			} else {
				r.Recorder.Event(dbUser, corev1.EventTypeNormal, "DatabaseUserDropped",
					fmt.Sprintf("User %s dropped from database %s", dbUser.Spec.UserName, dbUser.Spec.DatabaseName))
			}
		}
	}

	controllerutil.RemoveFinalizer(dbUser, v1alpha1.Finalizer)
	if err := r.Update(ctx, dbUser); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *DatabaseUserReconciler) getCredentials(ctx context.Context, namespace, secretName string) (string, string, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &secret); err != nil {
		return "", "", err
	}
	username, ok := secret.Data["username"]
	if !ok {
		return "", "", fmt.Errorf("secret %q missing 'username' key", secretName)
	}
	password, ok := secret.Data["password"]
	if !ok {
		return "", "", fmt.Errorf("secret %q missing 'password' key", secretName)
	}
	return string(username), string(password), nil
}

func (r *DatabaseUserReconciler) setConditionAndReturn(ctx context.Context, dbUser *v1alpha1.DatabaseUser,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	meta.SetStatusCondition(&dbUser.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: dbUser.Generation,
	})
	dbUser.Status.ObservedGeneration = dbUser.Generation

	if err := r.Status().Update(ctx, dbUser); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DatabaseUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DatabaseUser{}).
		Complete(r)
}
