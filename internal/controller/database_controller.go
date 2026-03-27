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

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

const requeueInterval = 30 * time.Second

// DatabaseReconciler reconciles a Database object.
type DatabaseReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	SQLClientFactory sqlclient.ClientFactory
}

// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the Database CR
	var db v1alpha1.Database
	if err := r.Get(ctx, req.NamespacedName, &db); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Finalizer handling
	if db.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &db)
	}

	if !controllerutil.ContainsFinalizer(&db, v1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&db, v1alpha1.Finalizer)
		if err := r.Update(ctx, &db); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Read the credentials Secret
	username, password, err := r.getCredentials(ctx, db.Namespace, db.Spec.Server.CredentialsSecret.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonSecretNotFound,
				fmt.Sprintf("Secret %q not found", db.Spec.Server.CredentialsSecret.Name))
		}
		return r.setConditionAndReturn(ctx, &db, metav1.ConditionFalse, v1alpha1.ReasonInvalidCredentialsSecret, err.Error())
	}

	// 4. Connect to SQL Server
	port := int32(1433)
	if db.Spec.Server.Port != nil {
		port = *db.Spec.Server.Port
	}
	tlsEnabled := false
	if db.Spec.Server.TLS != nil {
		tlsEnabled = *db.Spec.Server.TLS
	}

	sqlClient, err := r.SQLClientFactory(db.Spec.Server.Host, int(port), username, password, tlsEnabled)
	if err != nil {
		logger.Error(err, "failed to connect to SQL Server")
		r.Recorder.Event(&db, corev1.EventTypeWarning, v1alpha1.ReasonConnectionFailed, err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to connect to SQL Server: %w", err)
	}
	defer sqlClient.Close()

	// 5. Observe current state
	exists, err := sqlClient.DatabaseExists(ctx, db.Spec.DatabaseName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check database existence: %w", err)
	}

	// 6. Compare and act
	if !exists {
		if err := sqlClient.CreateDatabase(ctx, db.Spec.DatabaseName, db.Spec.Collation); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create database %s: %w", db.Spec.DatabaseName, err)
		}
		r.Recorder.Event(&db, corev1.EventTypeNormal, "DatabaseCreated",
			fmt.Sprintf("Database %s created", db.Spec.DatabaseName))
		logger.Info("database created", "database", db.Spec.DatabaseName)
	}

	// Update owner if specified and different
	if db.Spec.Owner != nil {
		currentOwner, err := sqlClient.GetDatabaseOwner(ctx, db.Spec.DatabaseName)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get database owner: %w", err)
		}
		if currentOwner != *db.Spec.Owner {
			if err := sqlClient.SetDatabaseOwner(ctx, db.Spec.DatabaseName, *db.Spec.Owner); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set database owner: %w", err)
			}
			r.Recorder.Event(&db, corev1.EventTypeNormal, "DatabaseOwnerUpdated",
				fmt.Sprintf("Database %s owner set to %s", db.Spec.DatabaseName, *db.Spec.Owner))
		}
	}

	// 7. Status: Ready=True
	return r.setConditionAndReturn(ctx, &db, metav1.ConditionTrue, v1alpha1.ReasonReady,
		fmt.Sprintf("Database %s is ready", db.Spec.DatabaseName))
}

func (r *DatabaseReconciler) handleDeletion(ctx context.Context, db *v1alpha1.Database) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(db, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	// Determine deletion policy (default: Retain)
	policy := v1alpha1.DeletionPolicyRetain
	if db.Spec.DeletionPolicy != nil {
		policy = *db.Spec.DeletionPolicy
	}

	if policy == v1alpha1.DeletionPolicyDelete {
		username, password, err := r.getCredentials(ctx, db.Namespace, db.Spec.Server.CredentialsSecret.Name)
		if err != nil {
			logger.Error(err, "failed to get credentials for cleanup, removing finalizer anyway")
		} else {
			port := int32(1433)
			if db.Spec.Server.Port != nil {
				port = *db.Spec.Server.Port
			}
			tlsEnabled := false
			if db.Spec.Server.TLS != nil {
				tlsEnabled = *db.Spec.Server.TLS
			}
			sqlClient, err := r.SQLClientFactory(db.Spec.Server.Host, int(port), username, password, tlsEnabled)
			if err != nil {
				logger.Error(err, "failed to connect to SQL Server for cleanup, removing finalizer anyway")
			} else {
				defer sqlClient.Close()
				if err := sqlClient.DropDatabase(ctx, db.Spec.DatabaseName); err != nil {
					logger.Error(err, "failed to drop database, removing finalizer anyway")
				} else {
					r.Recorder.Event(db, corev1.EventTypeNormal, "DatabaseDropped",
						fmt.Sprintf("Database %s dropped", db.Spec.DatabaseName))
					logger.Info("database dropped", "database", db.Spec.DatabaseName)
				}
			}
		}
	}

	controllerutil.RemoveFinalizer(db, v1alpha1.Finalizer)
	if err := r.Update(ctx, db); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *DatabaseReconciler) getCredentials(ctx context.Context, namespace, secretName string) (string, string, error) {
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

func (r *DatabaseReconciler) setConditionAndReturn(ctx context.Context, db *v1alpha1.Database,
	status metav1.ConditionStatus, reason, message string) (ctrl.Result, error) {

	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: db.Generation,
	})
	db.Status.ObservedGeneration = db.Generation

	if err := r.Status().Update(ctx, db); err != nil {
		return ctrl.Result{}, err
	}

	if status == metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Database{}).
		Complete(r)
}
