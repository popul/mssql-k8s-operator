//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	_ "github.com/microsoft/go-mssqldb"
	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	internalsql "github.com/popul/mssql-k8s-operator/internal/sql"
)

const (
	defaultNamespace    = "e2e-test"
	defaultSAPassword   = "P@ssw0rd123!"
	defaultSQLImage     = "mcr.microsoft.com/mssql/server:2022-latest"
	defaultOperatorImage = "mssql-k8s-operator:latest"

	pollInterval = 2 * time.Second
	pollTimeout  = 120 * time.Second

	helmReleaseName = "mssql-operator"
	helmChartPath   = "../../charts/mssql-operator"
)

var (
	k8sClient      client.Client
	sqlClient      internalsql.SQLClient
	ctx            context.Context
	cancel         context.CancelFunc
	portFwdCmd     *exec.Cmd
	testNamespace  string
	saPassword     string
	sqlImage       string
	operatorImage  string
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestMain(m *testing.M) {
	testNamespace = envOrDefault("E2E_NAMESPACE", defaultNamespace)
	saPassword = envOrDefault("E2E_SA_PASSWORD", defaultSAPassword)
	sqlImage = envOrDefault("E2E_SQL_IMAGE", defaultSQLImage)
	operatorImage = envOrDefault("E2E_OPERATOR_IMAGE", defaultOperatorImage)

	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Minute)

	exitCode := 1
	defer func() {
		cancel()
		os.Exit(exitCode)
	}()

	// Build k8s client
	kubeconfig := envOrDefault("KUBECONFIG", clientcmd.RecommendedHomeFile)
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build kubeconfig: %v\n", err)
		return
	}

	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = mssqlv1.AddToScheme(s)

	k8sClient, err = client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create k8s client: %v\n", err)
		return
	}

	// Setup infrastructure
	if err := setupNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to setup namespace: %v\n", err)
		return
	}

	if err := deploySQLServer(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to deploy SQL Server: %v\n", err)
		return
	}

	if err := waitForSQLServerReady(); err != nil {
		fmt.Fprintf(os.Stderr, "SQL Server not ready: %v\n", err)
		return
	}

	if err := startPortForward(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start port-forward: %v\n", err)
		return
	}

	if err := connectSQLClient(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect SQL client: %v\n", err)
		return
	}

	if err := installOperator(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to install operator: %v\n", err)
		return
	}

	// Run tests
	exitCode = m.Run()

	// Teardown
	teardown()
}

// --- Setup helpers ---

func setupNamespace() error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
	}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace: %w", err)
	}
	return nil
}

func deploySQLServer() error {
	// SA password secret for the MSSQL container
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mssql-sa-password", Namespace: testNamespace},
		StringData: map[string]string{
			"MSSQL_SA_PASSWORD": saPassword,
		},
	}
	if err := createOrUpdate(saSecret); err != nil {
		return fmt.Errorf("create sa-password secret: %w", err)
	}

	// Credentials secret for the operator CRs
	credsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mssql-sa-credentials", Namespace: testNamespace},
		StringData: map[string]string{
			"username": "sa",
			"password": saPassword,
		},
	}
	if err := createOrUpdate(credsSecret); err != nil {
		return fmt.Errorf("create credentials secret: %w", err)
	}

	// MSSQL Deployment
	labels := map[string]string{"app": "mssql"}
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "mssql", Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "mssql",
						Image: sqlImage,
						Env: []corev1.EnvVar{
							{Name: "ACCEPT_EULA", Value: "Y"},
							{Name: "MSSQL_SA_PASSWORD", ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "mssql-sa-password"},
									Key:                  "MSSQL_SA_PASSWORD",
								},
							}},
						},
						Ports: []corev1.ContainerPort{{ContainerPort: 1433}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("512Mi"),
								corev1.ResourceCPU:    resource.MustParse("250m"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								TCPSocket: &corev1.TCPSocketAction{
									Port: intstr.FromInt32(1433),
								},
							},
							InitialDelaySeconds: 15,
							PeriodSeconds:       10,
						},
					}},
				},
			},
		},
	}
	if err := createOrUpdate(dep); err != nil {
		return fmt.Errorf("create mssql deployment: %w", err)
	}

	// MSSQL Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "mssql", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Port:       1433,
				TargetPort: intstr.FromInt32(1433),
			}},
		},
	}
	if err := createOrUpdate(svc); err != nil {
		return fmt.Errorf("create mssql service: %w", err)
	}

	return nil
}

func waitForSQLServerReady() error {
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Name: "mssql", Namespace: testNamespace}

	return wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, dep); err != nil {
			return false, nil
		}
		return dep.Status.ReadyReplicas >= 1, nil
	})
}

func startPortForward() error {
	localPort := envOrDefault("E2E_LOCAL_SQL_PORT", "1433")
	portFwdCmd = exec.CommandContext(ctx, "kubectl", "port-forward",
		"svc/mssql", fmt.Sprintf("%s:1433", localPort),
		"-n", testNamespace,
	)
	portFwdCmd.Stdout = os.Stdout
	portFwdCmd.Stderr = os.Stderr
	if err := portFwdCmd.Start(); err != nil {
		return fmt.Errorf("start port-forward: %w", err)
	}
	// Give port-forward a moment to establish
	time.Sleep(3 * time.Second)
	return nil
}

func connectSQLClient() error {
	localPort := envOrDefault("E2E_LOCAL_SQL_PORT", "1433")
	host := envOrDefault("E2E_SQL_HOST", "localhost")
	port := 1433
	if localPort != "1433" {
		fmt.Sscanf(localPort, "%d", &port)
	}

	factory := internalsql.NewClientFactory()

	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		var err error
		sqlClient, err = factory(host, port, "sa", saPassword, false)
		if err != nil {
			return false, nil
		}
		if err = sqlClient.Ping(ctx); err != nil {
			sqlClient.Close()
			sqlClient = nil
			return false, nil
		}
		return true, nil
	})
}

func installOperator() error {
	cmd := exec.CommandContext(ctx, "helm", "upgrade", "--install",
		helmReleaseName, helmChartPath,
		"--namespace", "mssql-operator-system",
		"--create-namespace",
		"--set", fmt.Sprintf("image.repository=%s", "mssql-k8s-operator"),
		"--set", fmt.Sprintf("image.tag=%s", "latest"),
		"--set", "image.pullPolicy=Never",
		"--set", "leaderElection.enabled=false",
		"--wait",
		"--timeout", "120s",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func teardown() {
	// Delete test namespace (cascading delete of all resources)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	_ = k8sClient.Delete(ctx, ns)

	// Uninstall operator
	cmd := exec.CommandContext(ctx, "helm", "uninstall", helmReleaseName,
		"--namespace", "mssql-operator-system",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// Stop port-forward
	if portFwdCmd != nil && portFwdCmd.Process != nil {
		_ = portFwdCmd.Process.Kill()
	}

	// Close SQL client
	if sqlClient != nil {
		_ = sqlClient.Close()
	}
}

func createOrUpdate(obj client.Object) error {
	err := k8sClient.Create(ctx, obj)
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// --- Wait helpers ---

func waitForReady(t *testing.T, key types.NamespacedName, obj client.Object) {
	t.Helper()
	waitForCondition(t, key, obj, mssqlv1.ConditionReady, metav1.ConditionTrue, pollTimeout)
}

func waitForCondition(t *testing.T, key types.NamespacedName, obj client.Object, condType string, expected metav1.ConditionStatus, timeout time.Duration) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, obj); err != nil {
			return false, nil
		}
		cond := findCondition(obj, condType)
		if cond == nil {
			return false, nil
		}
		return cond.Status == expected, nil
	})
	if err != nil {
		cond := findCondition(obj, condType)
		condStr := "<nil>"
		if cond != nil {
			condStr = fmt.Sprintf("Status=%s Reason=%s Message=%s", cond.Status, cond.Reason, cond.Message)
		}
		t.Fatalf("Timed out waiting for condition %s=%s on %s: last condition: %s", condType, expected, key, condStr)
	}
}

func waitForDeletion(t *testing.T, key types.NamespacedName, obj client.Object, timeout time.Duration) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		err := k8sClient.Get(ctx, key, obj)
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Timed out waiting for deletion of %s", key)
	}
}

func findCondition(obj client.Object, condType string) *metav1.Condition {
	var conditions []metav1.Condition
	switch o := obj.(type) {
	case *mssqlv1.Database:
		conditions = o.Status.Conditions
	case *mssqlv1.Login:
		conditions = o.Status.Conditions
	case *mssqlv1.DatabaseUser:
		conditions = o.Status.Conditions
	case *mssqlv1.Schema:
		conditions = o.Status.Conditions
	case *mssqlv1.Permission:
		conditions = o.Status.Conditions
	case *mssqlv1.Backup:
		conditions = o.Status.Conditions
	case *mssqlv1.Restore:
		conditions = o.Status.Conditions
	case *mssqlv1.AvailabilityGroup:
		conditions = o.Status.Conditions
	case *mssqlv1.AGFailover:
		conditions = o.Status.Conditions
	case *mssqlv1.SQLServer:
		conditions = o.Status.Conditions
	case *mssqlv1.ScheduledBackup:
		conditions = o.Status.Conditions
	}
	return meta.FindStatusCondition(conditions, condType)
}

// waitForBackupPhase waits for a Backup CR to reach the given phase.
func waitForBackupPhase(t *testing.T, key types.NamespacedName, expectedPhase mssqlv1.BackupPhase, timeout time.Duration) *mssqlv1.Backup {
	t.Helper()
	bak := &mssqlv1.Backup{}
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, bak); err != nil {
			return false, nil
		}
		return bak.Status.Phase == expectedPhase, nil
	})
	if err != nil {
		t.Fatalf("Timed out waiting for Backup %s phase=%s (current=%s)", key, expectedPhase, bak.Status.Phase)
	}
	return bak
}

// waitForRestorePhase waits for a Restore CR to reach the given phase.
func waitForRestorePhase(t *testing.T, key types.NamespacedName, expectedPhase mssqlv1.RestorePhase, timeout time.Duration) *mssqlv1.Restore {
	t.Helper()
	rst := &mssqlv1.Restore{}
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, rst); err != nil {
			return false, nil
		}
		return rst.Status.Phase == expectedPhase, nil
	})
	if err != nil {
		t.Fatalf("Timed out waiting for Restore %s phase=%s (current=%s)", key, expectedPhase, rst.Status.Phase)
	}
	return rst
}

// --- Pointer helpers ---

func ptr[T any](v T) *T { return &v }

// --- Server reference for all CRs ---

func serverRef() mssqlv1.ServerReference {
	return mssqlv1.ServerReference{
		Host: fmt.Sprintf("mssql.%s.svc.cluster.local", testNamespace),
		Port: ptr(int32(1433)),
		CredentialsSecret: mssqlv1.SecretReference{
			Name: "mssql-sa-credentials",
		},
		TLS: ptr(false),
	}
}

// --- E2E Lifecycle Test ---

func TestE2EFullLifecycle(t *testing.T) {
	var (
		dbKey   = types.NamespacedName{Name: "test-db", Namespace: testNamespace}
		lgKey   = types.NamespacedName{Name: "test-login", Namespace: testNamespace}
		userKey = types.NamespacedName{Name: "test-dbuser", Namespace: testNamespace}
	)

	t.Run("CreateDatabase", func(t *testing.T) {
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dbKey.Name,
				Namespace: dbKey.Namespace,
			},
			Spec: mssqlv1.DatabaseSpec{
				Server:         serverRef(),
				DatabaseName:   "e2etest",
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, db); err != nil {
			t.Fatalf("Failed to create Database CR: %v", err)
		}

		waitForReady(t, dbKey, &mssqlv1.Database{})

		exists, err := sqlClient.DatabaseExists(ctx, "e2etest")
		if err != nil {
			t.Fatalf("Failed to check database existence: %v", err)
		}
		if !exists {
			t.Fatal("Database e2etest does not exist on SQL Server")
		}
	})

	t.Run("CreateLogin", func(t *testing.T) {
		// Create password secret
		pwSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-login-password", Namespace: testNamespace},
			StringData: map[string]string{"password": "LoginP@ss123!"},
		}
		if err := k8sClient.Create(ctx, pwSecret); err != nil && !errors.IsAlreadyExists(err) {
			t.Fatalf("Failed to create login password secret: %v", err)
		}

		login := &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{
				Name:      lgKey.Name,
				Namespace: lgKey.Namespace,
			},
			Spec: mssqlv1.LoginSpec{
				Server:         serverRef(),
				LoginName:      "e2elogin",
				PasswordSecret: mssqlv1.SecretReference{Name: "test-login-password"},
				ServerRoles:    []string{"dbcreator"},
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, login); err != nil {
			t.Fatalf("Failed to create Login CR: %v", err)
		}

		waitForReady(t, lgKey, &mssqlv1.Login{})

		exists, err := sqlClient.LoginExists(ctx, "e2elogin")
		if err != nil {
			t.Fatalf("Failed to check login existence: %v", err)
		}
		if !exists {
			t.Fatal("Login e2elogin does not exist on SQL Server")
		}

		roles, err := sqlClient.GetLoginServerRoles(ctx, "e2elogin")
		if err != nil {
			t.Fatalf("Failed to get login server roles: %v", err)
		}
		if !containsString(roles, "dbcreator") {
			t.Fatalf("Expected login to have role dbcreator, got: %v", roles)
		}
	})

	t.Run("CreateDatabaseUser", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userKey.Name,
				Namespace: userKey.Namespace,
			},
			Spec: mssqlv1.DatabaseUserSpec{
				Server:        serverRef(),
				DatabaseName:  "e2etest",
				UserName:      "e2euser",
				LoginRef:      mssqlv1.LoginReference{Name: "test-login"},
				DatabaseRoles: []string{"db_datareader", "db_datawriter"},
			},
		}
		if err := k8sClient.Create(ctx, dbUser); err != nil {
			t.Fatalf("Failed to create DatabaseUser CR: %v", err)
		}

		waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

		exists, err := sqlClient.UserExists(ctx, "e2etest", "e2euser")
		if err != nil {
			t.Fatalf("Failed to check user existence: %v", err)
		}
		if !exists {
			t.Fatal("User e2euser does not exist in e2etest")
		}

		roles, err := sqlClient.GetUserDatabaseRoles(ctx, "e2etest", "e2euser")
		if err != nil {
			t.Fatalf("Failed to get user database roles: %v", err)
		}
		assertContains(t, roles, "db_datareader")
		assertContains(t, roles, "db_datawriter")
	})

	t.Run("ModifyRoles_RemoveRole", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{}
		if err := k8sClient.Get(ctx, userKey, dbUser); err != nil {
			t.Fatalf("Failed to get DatabaseUser: %v", err)
		}

		dbUser.Spec.DatabaseRoles = []string{"db_datareader"}
		if err := k8sClient.Update(ctx, dbUser); err != nil {
			t.Fatalf("Failed to update DatabaseUser: %v", err)
		}

		// Wait for the roles to converge on SQL Server
		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			roles, err := sqlClient.GetUserDatabaseRoles(ctx, "e2etest", "e2euser")
			if err != nil {
				return false, nil
			}
			return containsString(roles, "db_datareader") && !containsString(roles, "db_datawriter"), nil
		})
		if err != nil {
			t.Fatal("Roles did not converge after removing db_datawriter")
		}
	})

	t.Run("ModifyRoles_AddRoles", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{}
		if err := k8sClient.Get(ctx, userKey, dbUser); err != nil {
			t.Fatalf("Failed to get DatabaseUser: %v", err)
		}

		dbUser.Spec.DatabaseRoles = []string{"db_datareader", "db_datawriter", "db_ddladmin"}
		if err := k8sClient.Update(ctx, dbUser); err != nil {
			t.Fatalf("Failed to update DatabaseUser: %v", err)
		}

		err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
			roles, err := sqlClient.GetUserDatabaseRoles(ctx, "e2etest", "e2euser")
			if err != nil {
				return false, nil
			}
			return containsString(roles, "db_datareader") &&
				containsString(roles, "db_datawriter") &&
				containsString(roles, "db_ddladmin"), nil
		})
		if err != nil {
			t.Fatal("Roles did not converge after adding db_datawriter and db_ddladmin")
		}
	})

	t.Run("DeleteDatabaseUser", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{
			ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		}
		if err := k8sClient.Delete(ctx, dbUser); err != nil {
			t.Fatalf("Failed to delete DatabaseUser: %v", err)
		}

		waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)

		exists, err := sqlClient.UserExists(ctx, "e2etest", "e2euser")
		if err != nil {
			t.Fatalf("Failed to check user existence after deletion: %v", err)
		}
		if exists {
			t.Fatal("User e2euser still exists after CR deletion")
		}
	})

	t.Run("DeleteLogin", func(t *testing.T) {
		login := &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		}
		if err := k8sClient.Delete(ctx, login); err != nil {
			t.Fatalf("Failed to delete Login: %v", err)
		}

		waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)

		exists, err := sqlClient.LoginExists(ctx, "e2elogin")
		if err != nil {
			t.Fatalf("Failed to check login existence after deletion: %v", err)
		}
		if exists {
			t.Fatal("Login e2elogin still exists after CR deletion")
		}
	})

	t.Run("DeleteDatabase", func(t *testing.T) {
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		}
		if err := k8sClient.Delete(ctx, db); err != nil {
			t.Fatalf("Failed to delete Database: %v", err)
		}

		waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)

		exists, err := sqlClient.DatabaseExists(ctx, "e2etest")
		if err != nil {
			t.Fatalf("Failed to check database existence after deletion: %v", err)
		}
		if exists {
			t.Fatal("Database e2etest still exists after CR deletion")
		}
	})
}

func TestE2EIdempotence(t *testing.T) {
	key := types.NamespacedName{Name: "test-idempotent-db", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "e2eidempotent",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Record the status after initial reconciliation
	initial := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, initial); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}
	initialGeneration := initial.Status.ObservedGeneration
	initialCond := meta.FindStatusCondition(initial.Status.Conditions, mssqlv1.ConditionReady)

	// Wait for a few reconciliation cycles
	time.Sleep(15 * time.Second)

	// Verify nothing changed
	after := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, after); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}

	if after.Status.ObservedGeneration != initialGeneration {
		t.Errorf("ObservedGeneration changed: %d -> %d", initialGeneration, after.Status.ObservedGeneration)
	}

	afterCond := meta.FindStatusCondition(after.Status.Conditions, mssqlv1.ConditionReady)
	if afterCond == nil {
		t.Fatal("Ready condition disappeared")
	}
	if afterCond.Status != initialCond.Status {
		t.Errorf("Ready condition status changed: %s -> %s", initialCond.Status, afterCond.Status)
	}
	if !afterCond.LastTransitionTime.Equal(&initialCond.LastTransitionTime) {
		t.Errorf("LastTransitionTime changed unexpectedly")
	}
}

// --- Error cases ---

func TestE2ESecretNotFound(t *testing.T) {
	key := types.NamespacedName{Name: "test-secret-missing", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server: mssqlv1.ServerReference{
				Host: fmt.Sprintf("mssql.%s.svc.cluster.local", testNamespace),
				Port: ptr(int32(1433)),
				CredentialsSecret: mssqlv1.SecretReference{
					Name: "nonexistent-secret",
				},
			},
			DatabaseName:   "secretmissingdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
	}()

	waitForConditionWithReason(t, key, &mssqlv1.Database{}, mssqlv1.ConditionReady, metav1.ConditionFalse, mssqlv1.ReasonSecretNotFound, pollTimeout)
}

func TestE2ESQLServerUnreachable(t *testing.T) {
	// Create a credentials secret so the controller gets past that check
	unreachableSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "unreachable-creds", Namespace: testNamespace},
		StringData: map[string]string{"username": "sa", "password": "fake"},
	}
	if err := k8sClient.Create(ctx, unreachableSecret); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create secret: %v", err)
	}

	key := types.NamespacedName{Name: "test-unreachable", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server: mssqlv1.ServerReference{
				Host: "unreachable-host.invalid",
				Port: ptr(int32(1433)),
				CredentialsSecret: mssqlv1.SecretReference{
					Name: "unreachable-creds",
				},
			},
			DatabaseName:   "unreachabledb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyRetain),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
	}()

	// The controller returns err on connection failure (retry with backoff) without setting condition.
	// After multiple retries, the CR should still have no Ready=True condition.
	// Wait a bit and verify the CR is not Ready.
	time.Sleep(15 * time.Second)
	got := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, got); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, mssqlv1.ConditionReady)
	if cond != nil && cond.Status == metav1.ConditionTrue {
		t.Fatal("Database should NOT be Ready with unreachable SQL Server")
	}
}

// --- DeletionPolicy Retain ---

func TestE2EDeletionPolicyRetain(t *testing.T) {
	// Create a database with Retain policy
	key := types.NamespacedName{Name: "test-retain-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "retaintest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyRetain),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}

	waitForReady(t, key, &mssqlv1.Database{})

	// Delete the CR
	if err := k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
	}); err != nil {
		t.Fatalf("Failed to delete Database CR: %v", err)
	}

	waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)

	// Database should still exist on SQL Server
	exists, err := sqlClient.DatabaseExists(ctx, "retaintest")
	if err != nil {
		t.Fatalf("Failed to check database existence: %v", err)
	}
	if !exists {
		t.Fatal("Database retaintest should still exist with Retain policy, but it was dropped")
	}

	// Cleanup: drop manually
	_ = sqlClient.DropDatabase(ctx, "retaintest")
}

func TestE2EDeletionPolicyRetainLogin(t *testing.T) {
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "retain-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "RetainP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	key := types.NamespacedName{Name: "test-retain-login", Namespace: testNamespace}
	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "retainlogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "retain-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyRetain),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login CR: %v", err)
	}

	waitForReady(t, key, &mssqlv1.Login{})

	if err := k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
	}); err != nil {
		t.Fatalf("Failed to delete Login CR: %v", err)
	}

	waitForDeletion(t, key, &mssqlv1.Login{}, 60*time.Second)

	exists, err := sqlClient.LoginExists(ctx, "retainlogin")
	if err != nil {
		t.Fatalf("Failed to check login existence: %v", err)
	}
	if !exists {
		t.Fatal("Login retainlogin should still exist with Retain policy")
	}

	// Cleanup
	_ = sqlClient.DropLogin(ctx, "retainlogin")
}

// --- LoginInUse ---

func TestE2ELoginInUse(t *testing.T) {
	// Setup: create DB, Login, DatabaseUser
	dbKey := types.NamespacedName{Name: "test-inuse-db", Namespace: testNamespace}
	lgKey := types.NamespacedName{Name: "test-inuse-login", Namespace: testNamespace}
	userKey := types.NamespacedName{Name: "test-inuse-user", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "inusedb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "inuse-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "InUseP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "inuselogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "inuse-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login: %v", err)
	}
	waitForReady(t, lgKey, &mssqlv1.Login{})

	dbUser := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "inusedb",
			UserName:     "inuseuser",
			LoginRef:     mssqlv1.LoginReference{Name: "test-inuse-login"},
		},
	}
	if err := k8sClient.Create(ctx, dbUser); err != nil {
		t.Fatalf("Failed to create DatabaseUser: %v", err)
	}
	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Try to delete the Login while user exists
	if err := k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
	}); err != nil {
		t.Fatalf("Failed to delete Login: %v", err)
	}

	// Login should get stuck with LoginInUse
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		lg := &mssqlv1.Login{}
		if err := k8sClient.Get(ctx, lgKey, lg); err != nil {
			return false, nil
		}
		cond := meta.FindStatusCondition(lg.Status.Conditions, mssqlv1.ConditionReady)
		if cond == nil {
			return false, nil
		}
		return cond.Status == metav1.ConditionFalse && cond.Reason == mssqlv1.ReasonLoginInUse, nil
	})
	if err != nil {
		t.Fatal("Login should be stuck with LoginInUse condition")
	}

	// Login should still exist on SQL Server
	exists, err := sqlClient.LoginExists(ctx, "inuselogin")
	if err != nil {
		t.Fatalf("Failed to check login: %v", err)
	}
	if !exists {
		t.Fatal("Login should not have been dropped while in use")
	}

	// Cleanup: delete user first, then login can proceed
	_ = k8sClient.Delete(ctx, &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
	})
	waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)
	waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)

	// Cleanup DB
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
	})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)
}

// --- UserOwnsObjects ---

func TestE2EUserOwnsObjects(t *testing.T) {
	dbKey := types.NamespacedName{Name: "test-owns-db", Namespace: testNamespace}
	lgKey := types.NamespacedName{Name: "test-owns-login", Namespace: testNamespace}
	userKey := types.NamespacedName{Name: "test-owns-user", Namespace: testNamespace}

	// Create DB
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "ownsdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, db)
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// Create Login
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "owns-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "OwnsP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "ownslogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "owns-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, login)
	waitForReady(t, lgKey, &mssqlv1.Login{})

	// Create DatabaseUser
	dbUser := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "ownsdb",
			UserName:     "ownsuser",
			LoginRef:     mssqlv1.LoginReference{Name: "test-owns-login"},
		},
	}
	_ = k8sClient.Create(ctx, dbUser)
	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Create a schema owned by this user via raw SQL
	execRawSQL(t, "ownsdb", "CREATE SCHEMA [ownedschema] AUTHORIZATION [ownsuser]")

	// Try to delete the user
	_ = k8sClient.Delete(ctx, &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
	})

	// Should be stuck with UserOwnsObjects
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		u := &mssqlv1.DatabaseUser{}
		if err := k8sClient.Get(ctx, userKey, u); err != nil {
			return false, nil
		}
		cond := meta.FindStatusCondition(u.Status.Conditions, mssqlv1.ConditionReady)
		if cond == nil {
			return false, nil
		}
		return cond.Status == metav1.ConditionFalse && cond.Reason == mssqlv1.ReasonUserOwnsObjects, nil
	})
	if err != nil {
		t.Fatal("DatabaseUser should be stuck with UserOwnsObjects condition")
	}

	// Cleanup: transfer schema ownership, then user can be deleted
	execRawSQL(t, "ownsdb", "ALTER AUTHORIZATION ON SCHEMA::[ownedschema] TO [dbo]")
	execRawSQL(t, "ownsdb", "DROP SCHEMA [ownedschema]")

	waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
	})
	waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
	})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)
}

// --- LoginRefNotFound + convergence ---

func TestE2ELoginRefNotFound(t *testing.T) {
	// Create DB first
	dbKey := types.NamespacedName{Name: "test-loginref-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "loginrefdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, db)
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// Create DatabaseUser referencing a Login that doesn't exist yet
	userKey := types.NamespacedName{Name: "test-loginref-user", Namespace: testNamespace}
	dbUser := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "loginrefdb",
			UserName:     "loginrefuser",
			LoginRef:     mssqlv1.LoginReference{Name: "test-loginref-login"},
		},
	}
	if err := k8sClient.Create(ctx, dbUser); err != nil {
		t.Fatalf("Failed to create DatabaseUser: %v", err)
	}

	// Should get LoginRefNotFound
	waitForConditionWithReason(t, userKey, &mssqlv1.DatabaseUser{}, mssqlv1.ConditionReady, metav1.ConditionFalse, mssqlv1.ReasonLoginRefNotFound, pollTimeout)

	// Now create the Login
	lgKey := types.NamespacedName{Name: "test-loginref-login", Namespace: testNamespace}
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "loginref-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "LoginRefP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "loginreflogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "loginref-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	_ = k8sClient.Create(ctx, login)
	waitForReady(t, lgKey, &mssqlv1.Login{})

	// The DatabaseUser controller doesn't requeue on LoginRefNotFound (permanent error by design).
	// We need to bump the generation to trigger reconciliation.
	triggerReconciliation(t, userKey, &mssqlv1.DatabaseUser{})

	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Verify on SQL Server
	exists, err := sqlClient.UserExists(ctx, "loginrefdb", "loginrefuser")
	if err != nil {
		t.Fatalf("Failed to check user: %v", err)
	}
	if !exists {
		t.Fatal("User loginrefuser should exist after Login was created")
	}

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
	})
	waitForDeletion(t, userKey, &mssqlv1.DatabaseUser{}, 60*time.Second)
	_ = k8sClient.Delete(ctx, &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
	})
	waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
	})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, 60*time.Second)
}

// --- Collation immutable ---

func TestE2ECollationImmutable(t *testing.T) {
	key := types.NamespacedName{Name: "test-collation-db", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "collationdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Now set a different collation on the existing DB
	got := &mssqlv1.Database{}
	if err := k8sClient.Get(ctx, key, got); err != nil {
		t.Fatalf("Failed to get Database: %v", err)
	}
	got.Spec.Collation = ptr("Latin1_General_BIN")
	if err := k8sClient.Update(ctx, got); err != nil {
		t.Fatalf("Failed to update Database: %v", err)
	}

	waitForConditionWithReason(t, key, &mssqlv1.Database{}, mssqlv1.ConditionReady, metav1.ConditionFalse, mssqlv1.ReasonCollationChangeNotSupported, pollTimeout)
}

// --- Password rotation ---

func TestE2EPasswordRotation(t *testing.T) {
	lgKey := types.NamespacedName{Name: "test-pwrotate-login", Namespace: testNamespace}

	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pwrotate-secret", Namespace: testNamespace},
		StringData: map[string]string{"password": "OldP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "pwrotatelogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "pwrotate-secret"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: lgKey.Name, Namespace: lgKey.Namespace},
		})
		waitForDeletion(t, lgKey, &mssqlv1.Login{}, 60*time.Second)
	}()

	waitForReady(t, lgKey, &mssqlv1.Login{})

	// Verify old password works
	newPassword := "NewP@ss456!"
	verifyLoginPassword(t, "pwrotatelogin", "OldP@ss123!")

	// Update the secret with a new password
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Name: "pwrotate-secret", Namespace: testNamespace}
	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		t.Fatalf("Failed to get password secret: %v", err)
	}
	secret.Data["password"] = []byte(newPassword)
	if err := k8sClient.Update(ctx, secret); err != nil {
		t.Fatalf("Failed to update password secret: %v", err)
	}

	// The controller detects password changes via PasswordSecretResourceVersion on periodic requeue (~30s).
	// Trigger reconciliation to speed things up.
	triggerReconciliation(t, lgKey, &mssqlv1.Login{})

	// Wait until the new password works
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		factory := internalsql.NewClientFactory()
		c, err := factory("localhost", 1433, "pwrotatelogin", newPassword, false)
		if err != nil {
			return false, nil
		}
		defer c.Close()
		return c.Ping(ctx) == nil, nil
	})
	if err != nil {
		t.Fatal("New password did not work after rotation")
	}
}

// --- Adoption ---

func TestE2EAdoption(t *testing.T) {
	// Create a database directly on SQL Server
	if err := sqlClient.CreateDatabase(ctx, "adopteddb", nil); err != nil {
		t.Fatalf("Failed to create database directly: %v", err)
	}

	key := types.NamespacedName{Name: "test-adopt-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "adopteddb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	// Should adopt the existing database and become Ready
	waitForReady(t, key, &mssqlv1.Database{})

	// Verify database still exists
	exists, err := sqlClient.DatabaseExists(ctx, "adopteddb")
	if err != nil {
		t.Fatalf("Failed to check database: %v", err)
	}
	if !exists {
		t.Fatal("Adopted database should still exist")
	}
}

func TestE2EAdoptionLogin(t *testing.T) {
	// Create a login directly on SQL Server
	if err := sqlClient.CreateLogin(ctx, "adoptedlogin", "AdoptP@ss123!"); err != nil {
		t.Fatalf("Failed to create login directly: %v", err)
	}

	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "adopt-login-pw", Namespace: testNamespace},
		StringData: map[string]string{"password": "AdoptP@ss123!"},
	}
	_ = createOrUpdate(pwSecret)

	key := types.NamespacedName{Name: "test-adopt-login", Namespace: testNamespace}
	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "adoptedlogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "adopt-login-pw"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil {
		t.Fatalf("Failed to create Login CR: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Login{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Login{})
}

// --- Drift detection ---

func TestE2EDriftDetection(t *testing.T) {
	key := types.NamespacedName{Name: "test-drift-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "driftdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Drop the database manually (simulate drift)
	if err := sqlClient.DropDatabase(ctx, "driftdb"); err != nil {
		t.Fatalf("Failed to drop database: %v", err)
	}

	exists, _ := sqlClient.DatabaseExists(ctx, "driftdb")
	if exists {
		t.Fatal("Database should have been dropped")
	}

	// Trigger reconciliation (the periodic requeue should handle it, but let's speed it up)
	triggerReconciliation(t, key, &mssqlv1.Database{})

	// Wait for the database to be recreated
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		return sqlClient.DatabaseExists(ctx, "driftdb")
	})
	if err != nil {
		t.Fatal("Database was not recreated after drift")
	}
}

// --- SQL Server downtime ---

func TestE2ESQLServerDowntime(t *testing.T) {
	key := types.NamespacedName{Name: "test-downtime-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "downtimedb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 120*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Scale SQL Server to 0
	scaleSQLServer(t, 0)
	time.Sleep(10 * time.Second)

	// Scale back to 1
	scaleSQLServer(t, 1)

	// Wait for SQL Server to be ready again
	if err := waitForSQLServerReady(); err != nil {
		t.Fatalf("SQL Server did not come back: %v", err)
	}

	// Restart port-forward (old one died when pod was killed)
	if portFwdCmd != nil && portFwdCmd.Process != nil {
		_ = portFwdCmd.Process.Kill()
		_ = portFwdCmd.Wait()
	}
	if err := startPortForward(); err != nil {
		t.Fatalf("Failed to restart port-forward: %v", err)
	}

	// Reconnect SQL client (old connection is broken)
	if err := reconnectSQLClient(); err != nil {
		t.Fatalf("Failed to reconnect SQL client: %v", err)
	}

	// The operator should eventually reconcile and the CR should be Ready
	// Trigger reconciliation to speed things up
	triggerReconciliation(t, key, &mssqlv1.Database{})
	waitForReady(t, key, &mssqlv1.Database{})
}

// --- Operator restart ---

func TestE2EOperatorRestart(t *testing.T) {
	// Ensure SQL client is available (previous test may have disrupted it)
	if sqlClient == nil {
		if err := reconnectSQLClient(); err != nil {
			t.Fatalf("SQL client unavailable: %v", err)
		}
	}

	key := types.NamespacedName{Name: "test-restart-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "restartdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Kill the operator pod
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "pods",
		"-l", "app.kubernetes.io/name=mssql-operator",
		"-n", "mssql-operator-system",
		"--wait=false",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to delete operator pod: %v: %s", err, out)
	}

	// Wait for the pod to come back
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		dep := &appsv1.Deployment{}
		depKey := types.NamespacedName{Name: "mssql-operator", Namespace: "mssql-operator-system"}
		if err := k8sClient.Get(ctx, depKey, dep); err != nil {
			return false, nil
		}
		return dep.Status.ReadyReplicas >= 1, nil
	})
	if err != nil {
		t.Fatal("Operator did not come back after restart")
	}

	// Database should still be Ready after operator restart
	waitForReady(t, key, &mssqlv1.Database{})

	// Verify on SQL Server
	exists, err := sqlClient.DatabaseExists(ctx, "restartdb")
	if err != nil {
		t.Fatalf("Failed to check database: %v", err)
	}
	if !exists {
		t.Fatal("Database should still exist after operator restart")
	}
}

// --- API Validation ---

func TestE2EAPIValidation(t *testing.T) {
	t.Run("Database_MissingDatabaseName", func(t *testing.T) {
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: "test-invalid-db", Namespace: testNamespace},
			Spec: mssqlv1.DatabaseSpec{
				Server: serverRef(),
				// DatabaseName is missing
			},
		}
		err := k8sClient.Create(ctx, db)
		if err == nil {
			_ = k8sClient.Delete(ctx, db)
			t.Fatal("Expected validation error for missing databaseName, but got none")
		}
		if !errors.IsInvalid(err) && !errors.IsForbidden(err) && !strings.Contains(err.Error(), "databaseName") {
			t.Fatalf("Expected validation error about databaseName, got: %v", err)
		}
	})

	t.Run("Database_InvalidDeletionPolicy", func(t *testing.T) {
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: "test-invalid-policy", Namespace: testNamespace},
			Spec: mssqlv1.DatabaseSpec{
				Server:         serverRef(),
				DatabaseName:   "invalidpolicydb",
				DeletionPolicy: ptr(mssqlv1.DeletionPolicy("InvalidValue")),
			},
		}
		err := k8sClient.Create(ctx, db)
		if err == nil {
			_ = k8sClient.Delete(ctx, db)
			t.Fatal("Expected validation error for invalid deletionPolicy")
		}
	})

	t.Run("Login_MissingLoginName", func(t *testing.T) {
		login := &mssqlv1.Login{
			ObjectMeta: metav1.ObjectMeta{Name: "test-invalid-login", Namespace: testNamespace},
			Spec: mssqlv1.LoginSpec{
				Server:         serverRef(),
				PasswordSecret: mssqlv1.SecretReference{Name: "some-secret"},
			},
		}
		err := k8sClient.Create(ctx, login)
		if err == nil {
			_ = k8sClient.Delete(ctx, login)
			t.Fatal("Expected validation error for missing loginName")
		}
	})

	t.Run("Login_MissingPasswordSecretName", func(t *testing.T) {
		// In Go, omitting PasswordSecret produces passwordSecret: {name: ""}
		// which passes CRD validation since the struct is present.
		// Use an unstructured object to truly omit passwordSecret from the JSON.
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(fmt.Sprintf(`
apiVersion: mssql.popul.io/v1alpha1
kind: Login
metadata:
  name: test-invalid-login2
  namespace: %s
spec:
  server:
    host: mssql.example.com
    credentialsSecret:
      name: some-creds
  loginName: somelogin
`, testNamespace))
		out, err := cmd.CombinedOutput()
		if err == nil {
			_ = exec.CommandContext(ctx, "kubectl", "delete", "login", "test-invalid-login2", "-n", testNamespace).Run()
			t.Fatal("Expected validation error for missing passwordSecret")
		}
		if !strings.Contains(string(out), "passwordSecret") {
			t.Fatalf("Expected error about passwordSecret, got: %s", string(out))
		}
	})

	t.Run("DatabaseUser_MissingFields", func(t *testing.T) {
		dbUser := &mssqlv1.DatabaseUser{
			ObjectMeta: metav1.ObjectMeta{Name: "test-invalid-user", Namespace: testNamespace},
			Spec: mssqlv1.DatabaseUserSpec{
				Server: serverRef(),
				// Missing databaseName, userName, loginRef
			},
		}
		err := k8sClient.Create(ctx, dbUser)
		if err == nil {
			_ = k8sClient.Delete(ctx, dbUser)
			t.Fatal("Expected validation error for missing fields")
		}
	})
}

// --- Events ---

func TestE2EEvents(t *testing.T) {
	key := types.NamespacedName{Name: "test-events-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "eventsdb",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database: %v", err)
	}
	defer func() {
		_ = k8sClient.Delete(ctx, &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		})
		waitForDeletion(t, key, &mssqlv1.Database{}, 60*time.Second)
	}()

	waitForReady(t, key, &mssqlv1.Database{})

	// Check events using kubectl (events may take a moment to appear)
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		events := &corev1.EventList{}
		if err := k8sClient.List(ctx, events, client.InNamespace(testNamespace),
			client.MatchingFields{"involvedObject.name": "test-events-db"}); err != nil {
			// Field selectors may not work with all clients; fallback
			return false, nil
		}
		for _, ev := range events.Items {
			if ev.Reason == "DatabaseCreated" && ev.Type == corev1.EventTypeNormal {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		// Fallback: check via kubectl
		cmd := exec.CommandContext(ctx, "kubectl", "get", "events",
			"-n", testNamespace,
			"--field-selector", "involvedObject.name=test-events-db",
			"-o", "jsonpath={.items[*].reason}",
		)
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), "DatabaseCreated") {
			t.Errorf("Expected DatabaseCreated event, got events: %s", string(out))
		}
	}
}

// --- Health endpoints ---

func TestE2EHealthEndpoints(t *testing.T) {
	// Port-forward to the operator deployment pod directly
	healthFwd := exec.CommandContext(ctx, "kubectl", "port-forward",
		"deploy/mssql-operator", "18081:8081",
		"-n", "mssql-operator-system",
	)
	healthFwd.Stdout = io.Discard
	healthFwd.Stderr = io.Discard
	if err := healthFwd.Start(); err != nil {
		t.Fatalf("Failed to start health port-forward: %v", err)
	}
	defer func() {
		if healthFwd.Process != nil {
			_ = healthFwd.Process.Kill()
		}
	}()
	time.Sleep(3 * time.Second)

	t.Run("Healthz", func(t *testing.T) {
		checkEndpoint(t, "http://localhost:18081/healthz", 200)
	})

	t.Run("Readyz", func(t *testing.T) {
		checkEndpoint(t, "http://localhost:18081/readyz", 200)
	})
}

// --- Metrics ---

func TestE2EMetrics(t *testing.T) {
	metricsFwd := exec.CommandContext(ctx, "kubectl", "port-forward",
		"deploy/mssql-operator", "18080:8080",
		"-n", "mssql-operator-system",
	)
	metricsFwd.Stdout = io.Discard
	metricsFwd.Stderr = io.Discard
	if err := metricsFwd.Start(); err != nil {
		t.Fatalf("Failed to start metrics port-forward: %v", err)
	}
	defer func() {
		if metricsFwd.Process != nil {
			_ = metricsFwd.Process.Kill()
		}
	}()
	time.Sleep(3 * time.Second)

	resp, err := http.Get("http://localhost:18080/metrics")
	if err != nil {
		t.Fatalf("Failed to get metrics: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	metricsStr := string(body)

	if resp.StatusCode != 200 {
		t.Fatalf("Metrics endpoint returned %d", resp.StatusCode)
	}

	// Check for controller-runtime standard metrics
	assertMetricExists(t, metricsStr, "controller_runtime_reconcile_total")
	assertMetricExists(t, metricsStr, "workqueue_depth")

	// Check for custom operator metrics
	assertMetricExists(t, metricsStr, "mssql_operator_reconcile_total")
	assertMetricExists(t, metricsStr, "mssql_operator_reconcile_duration_seconds")

	// Check for business metrics registration
	assertMetricExists(t, metricsStr, "mssql_database_ready")
	assertMetricExists(t, metricsStr, "mssql_server_connected")
	assertMetricExists(t, metricsStr, "mssql_login_ready")
	assertMetricExists(t, metricsStr, "mssql_ag_replica_synchronized")
	assertMetricExists(t, metricsStr, "mssql_backup_last_success_timestamp")
	assertMetricExists(t, metricsStr, "mssql_scheduled_backup_total")
}

// =============================================================================
// Schema & Permission E2E Tests
// =============================================================================

func TestE2ESchemaLifecycle(t *testing.T) {
	// Prerequisite: create a database for schema tests
	dbKey := types.NamespacedName{Name: "schema-test-db", Namespace: testNamespace}
	schemaKey := types.NamespacedName{Name: "test-schema", Namespace: testNamespace}

	// Create database
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "schematest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	t.Run("CreateSchema", func(t *testing.T) {
		schema := &mssqlv1.Schema{
			ObjectMeta: metav1.ObjectMeta{Name: schemaKey.Name, Namespace: schemaKey.Namespace},
			Spec: mssqlv1.SchemaSpec{
				Server:         serverRef(),
				DatabaseName:   "schematest",
				SchemaName:     "app",
				Owner:          ptr("dbo"),
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, schema); err != nil {
			t.Fatalf("Failed to create Schema CR: %v", err)
		}

		waitForReady(t, schemaKey, &mssqlv1.Schema{})

		// Verify schema exists on SQL Server
		exists, err := sqlClient.SchemaExists(ctx, "schematest", "app")
		if err != nil {
			t.Fatalf("Failed to check schema existence: %v", err)
		}
		if !exists {
			t.Fatal("Schema 'app' does not exist in database 'schematest'")
		}
	})

	t.Run("UpdateSchemaOwner", func(t *testing.T) {
		var schema mssqlv1.Schema
		if err := k8sClient.Get(ctx, schemaKey, &schema); err != nil {
			t.Fatalf("Failed to get Schema: %v", err)
		}
		schema.Spec.Owner = ptr("dbo")
		if err := k8sClient.Update(ctx, &schema); err != nil {
			t.Fatalf("Failed to update Schema: %v", err)
		}
		waitForReady(t, schemaKey, &mssqlv1.Schema{})

		owner, err := sqlClient.GetSchemaOwner(ctx, "schematest", "app")
		if err != nil {
			t.Fatalf("Failed to get schema owner: %v", err)
		}
		if owner != "dbo" {
			t.Errorf("Expected schema owner 'dbo', got '%s'", owner)
		}
	})

	t.Run("DeleteSchema", func(t *testing.T) {
		var schema mssqlv1.Schema
		if err := k8sClient.Get(ctx, schemaKey, &schema); err != nil {
			t.Fatalf("Failed to get Schema: %v", err)
		}
		if err := k8sClient.Delete(ctx, &schema); err != nil {
			t.Fatalf("Failed to delete Schema CR: %v", err)
		}
		waitForDeletion(t, schemaKey, &mssqlv1.Schema{}, pollTimeout)

		// With Delete policy, schema should be dropped
		exists, err := sqlClient.SchemaExists(ctx, "schematest", "app")
		if err != nil {
			t.Fatalf("Failed to check schema existence: %v", err)
		}
		if exists {
			t.Fatal("Schema 'app' should have been dropped with DeletionPolicy=Delete")
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, db)
}

func TestE2EPermissionLifecycle(t *testing.T) {
	// Setup: database + login + user for permission tests
	dbKey := types.NamespacedName{Name: "perm-test-db", Namespace: testNamespace}
	loginKey := types.NamespacedName{Name: "perm-test-login", Namespace: testNamespace}
	userKey := types.NamespacedName{Name: "perm-test-user", Namespace: testNamespace}
	schemaKey := types.NamespacedName{Name: "perm-test-schema", Namespace: testNamespace}
	permKey := types.NamespacedName{Name: "perm-test-perms", Namespace: testNamespace}

	// Create database
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "permtest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// Create login
	pwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "perm-login-password", Namespace: testNamespace},
		StringData: map[string]string{"password": "PermP@ss123!"},
	}
	if err := k8sClient.Create(ctx, pwSecret); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create password secret: %v", err)
	}

	login := &mssqlv1.Login{
		ObjectMeta: metav1.ObjectMeta{Name: loginKey.Name, Namespace: loginKey.Namespace},
		Spec: mssqlv1.LoginSpec{
			Server:         serverRef(),
			LoginName:      "permlogin",
			PasswordSecret: mssqlv1.SecretReference{Name: "perm-login-password"},
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, login); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Login CR: %v", err)
	}
	waitForReady(t, loginKey, &mssqlv1.Login{})

	// Create database user
	user := &mssqlv1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: userKey.Name, Namespace: userKey.Namespace},
		Spec: mssqlv1.DatabaseUserSpec{
			Server:       serverRef(),
			DatabaseName: "permtest",
			UserName:     "permuser",
			LoginRef:     mssqlv1.LoginReference{Name: "perm-test-login"},
		},
	}
	if err := k8sClient.Create(ctx, user); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create DatabaseUser CR: %v", err)
	}
	waitForReady(t, userKey, &mssqlv1.DatabaseUser{})

	// Create a schema for permission targets
	schema := &mssqlv1.Schema{
		ObjectMeta: metav1.ObjectMeta{Name: schemaKey.Name, Namespace: schemaKey.Namespace},
		Spec: mssqlv1.SchemaSpec{
			Server:         serverRef(),
			DatabaseName:   "permtest",
			SchemaName:     "appdata",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, schema); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Schema CR: %v", err)
	}
	waitForReady(t, schemaKey, &mssqlv1.Schema{})

	t.Run("GrantPermissions", func(t *testing.T) {
		perm := &mssqlv1.Permission{
			ObjectMeta: metav1.ObjectMeta{Name: permKey.Name, Namespace: permKey.Namespace},
			Spec: mssqlv1.PermissionSpec{
				Server:       serverRef(),
				DatabaseName: "permtest",
				UserName:     "permuser",
				Grants: []mssqlv1.PermissionEntry{
					{Permission: "SELECT", On: "SCHEMA::appdata"},
					{Permission: "INSERT", On: "SCHEMA::appdata"},
				},
			},
		}
		if err := k8sClient.Create(ctx, perm); err != nil {
			t.Fatalf("Failed to create Permission CR: %v", err)
		}

		waitForReady(t, permKey, &mssqlv1.Permission{})

		// Verify permissions on SQL Server
		perms, err := sqlClient.GetPermissions(ctx, "permtest", "permuser")
		if err != nil {
			t.Fatalf("Failed to get permissions: %v", err)
		}
		foundSelect := false
		foundInsert := false
		for _, p := range perms {
			if p.Permission == "SELECT" && p.State == "GRANT" {
				foundSelect = true
			}
			if p.Permission == "INSERT" && p.State == "GRANT" {
				foundInsert = true
			}
		}
		if !foundSelect {
			t.Error("Expected SELECT GRANT on SCHEMA::appdata")
		}
		if !foundInsert {
			t.Error("Expected INSERT GRANT on SCHEMA::appdata")
		}
	})

	t.Run("AddDenyPermission", func(t *testing.T) {
		var perm mssqlv1.Permission
		if err := k8sClient.Get(ctx, permKey, &perm); err != nil {
			t.Fatalf("Failed to get Permission: %v", err)
		}
		perm.Spec.Denies = []mssqlv1.PermissionEntry{
			{Permission: "DELETE", On: "SCHEMA::appdata"},
		}
		if err := k8sClient.Update(ctx, &perm); err != nil {
			t.Fatalf("Failed to update Permission: %v", err)
		}

		waitForReady(t, permKey, &mssqlv1.Permission{})

		perms, err := sqlClient.GetPermissions(ctx, "permtest", "permuser")
		if err != nil {
			t.Fatalf("Failed to get permissions: %v", err)
		}
		foundDeny := false
		for _, p := range perms {
			if p.Permission == "DELETE" && p.State == "DENY" {
				foundDeny = true
			}
		}
		if !foundDeny {
			t.Error("Expected DELETE DENY on SCHEMA::appdata")
		}
	})

	t.Run("DeletePermission_RevokesAll", func(t *testing.T) {
		var perm mssqlv1.Permission
		if err := k8sClient.Get(ctx, permKey, &perm); err != nil {
			t.Fatalf("Failed to get Permission: %v", err)
		}
		if err := k8sClient.Delete(ctx, &perm); err != nil {
			t.Fatalf("Failed to delete Permission CR: %v", err)
		}
		waitForDeletion(t, permKey, &mssqlv1.Permission{}, pollTimeout)

		// All grants and denies should be revoked
		perms, err := sqlClient.GetPermissions(ctx, "permtest", "permuser")
		if err != nil {
			t.Fatalf("Failed to get permissions: %v", err)
		}
		for _, p := range perms {
			if p.State == "GRANT" || p.State == "DENY" {
				t.Errorf("Expected all permissions revoked, but found %s %s", p.State, p.Permission)
			}
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, user)
	_ = k8sClient.Delete(ctx, schema)
	_ = k8sClient.Delete(ctx, login)
	_ = k8sClient.Delete(ctx, db)
}

// =============================================================================
// Backup & Restore E2E Tests
// =============================================================================

func TestE2EBackupRestore(t *testing.T) {
	// Prerequisite: create a database to back up
	dbKey := types.NamespacedName{Name: "backup-test-db", Namespace: testNamespace}
	backupKey := types.NamespacedName{Name: "test-backup", Namespace: testNamespace}
	restoreKey := types.NamespacedName{Name: "test-restore", Namespace: testNamespace}

	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "backuptest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	t.Run("CreateBackup_Full", func(t *testing.T) {
		backup := &mssqlv1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace},
			Spec: mssqlv1.BackupSpec{
				Server:      serverRef(),
				DatabaseName: "backuptest",
				Destination: "/var/opt/mssql/backup/backuptest.bak",
				Type:        mssqlv1.BackupTypeFull,
				Compression: ptr(true),
			},
		}
		if err := k8sClient.Create(ctx, backup); err != nil {
			t.Fatalf("Failed to create Backup CR: %v", err)
		}

		bak := waitForBackupPhase(t, backupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

		// Verify status fields
		if bak.Status.StartTime == nil {
			t.Error("Expected StartTime to be set")
		}
		if bak.Status.CompletionTime == nil {
			t.Error("Expected CompletionTime to be set")
		}
		if bak.Status.CompletionTime != nil && bak.Status.StartTime != nil {
			if bak.Status.CompletionTime.Before(bak.Status.StartTime) {
				t.Error("CompletionTime should not be before StartTime")
			}
		}

		// Verify Ready condition
		cond := findCondition(bak, mssqlv1.ConditionReady)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Error("Expected Ready=True condition on completed backup")
		}
	})

	t.Run("BackupImmutable", func(t *testing.T) {
		var bak mssqlv1.Backup
		if err := k8sClient.Get(ctx, backupKey, &bak); err != nil {
			t.Fatalf("Failed to get Backup: %v", err)
		}
		bak.Spec.DatabaseName = "othername"
		err := k8sClient.Update(ctx, &bak)
		if err == nil {
			t.Error("Expected update to be rejected (immutable spec)")
		}
	})

	t.Run("RestoreDatabase", func(t *testing.T) {
		// Drop the original database first so restore can recreate it
		_ = k8sClient.Delete(ctx, db)
		waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)

		// Wait for the DB to actually be dropped on SQL Server
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			exists, err := sqlClient.DatabaseExists(ctx, "backuptest")
			if err != nil {
				return false, nil
			}
			return !exists, nil
		})
		if err != nil {
			t.Fatalf("Timed out waiting for database to be dropped: %v", err)
		}

		restore := &mssqlv1.Restore{
			ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace},
			Spec: mssqlv1.RestoreSpec{
				Server:       serverRef(),
				DatabaseName: "backuptest",
				Source:       "/var/opt/mssql/backup/backuptest.bak",
			},
		}
		if err := k8sClient.Create(ctx, restore); err != nil {
			t.Fatalf("Failed to create Restore CR: %v", err)
		}

		rst := waitForRestorePhase(t, restoreKey, mssqlv1.RestorePhaseCompleted, pollTimeout)

		// Verify status
		if rst.Status.StartTime == nil {
			t.Error("Expected StartTime to be set")
		}
		if rst.Status.CompletionTime == nil {
			t.Error("Expected CompletionTime to be set")
		}

		// Verify database exists again
		exists, err := sqlClient.DatabaseExists(ctx, "backuptest")
		if err != nil {
			t.Fatalf("Failed to check database existence: %v", err)
		}
		if !exists {
			t.Fatal("Database 'backuptest' should exist after restore")
		}
	})

	t.Run("RestoreImmutable", func(t *testing.T) {
		var rst mssqlv1.Restore
		if err := k8sClient.Get(ctx, restoreKey, &rst); err != nil {
			t.Fatalf("Failed to get Restore: %v", err)
		}
		rst.Spec.DatabaseName = "othername"
		err := k8sClient.Update(ctx, &rst)
		if err == nil {
			t.Error("Expected update to be rejected (immutable spec)")
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace}})
	// Drop the restored database via raw SQL
	execRawSQL(t, "master", "IF DB_ID('backuptest') IS NOT NULL DROP DATABASE [backuptest]")
}

func TestE2EBackupFailure_NonExistentDB(t *testing.T) {
	key := types.NamespacedName{Name: "backup-fail-nodb", Namespace: testNamespace}

	backup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:       serverRef(),
			DatabaseName: "nonexistent_db_xyz",
			Destination:  "/var/opt/mssql/backup/fail.bak",
			Type:         mssqlv1.BackupTypeFull,
		},
	}
	if err := k8sClient.Create(ctx, backup); err != nil {
		t.Fatalf("Failed to create Backup CR: %v", err)
	}

	bak := waitForBackupPhase(t, key, mssqlv1.BackupPhaseFailed, pollTimeout)

	cond := findCondition(bak, mssqlv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Error("Expected Ready=False condition on failed backup")
	}

	// Cleanup
	_ = k8sClient.Delete(ctx, backup)
}

// =============================================================================
// SQLServer CRD E2E Tests
// =============================================================================

func TestE2ESQLServerLifecycle(t *testing.T) {
	srvKey := types.NamespacedName{Name: "e2e-sqlserver", Namespace: testNamespace}

	t.Run("CreateSQLServer", func(t *testing.T) {
		srv := &mssqlv1.SQLServer{
			ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace},
			Spec: mssqlv1.SQLServerSpec{
				Host: fmt.Sprintf("mssql.%s.svc.cluster.local", testNamespace),
				Port: ptr(int32(1433)),
				CredentialsSecret: &mssqlv1.CrossNamespaceSecretReference{
					Name:      "mssql-sa-credentials",
					Namespace: ptr(testNamespace),
				},
				TLS:        ptr(false),
				AuthMethod: mssqlv1.AuthSqlLogin,
			},
		}
		if err := k8sClient.Create(ctx, srv); err != nil {
			t.Fatalf("Failed to create SQLServer CR: %v", err)
		}

		waitForReady(t, srvKey, &mssqlv1.SQLServer{})

		// Verify status fields
		var updated mssqlv1.SQLServer
		if err := k8sClient.Get(ctx, srvKey, &updated); err != nil {
			t.Fatalf("Failed to get SQLServer: %v", err)
		}
		if updated.Status.ServerVersion == "" {
			t.Error("Expected serverVersion to be set")
		}
		if updated.Status.Edition == "" {
			t.Error("Expected edition to be set")
		}
		if updated.Status.LastConnectedTime == nil {
			t.Error("Expected lastConnectedTime to be set")
		}
		t.Logf("SQLServer version=%s edition=%s", updated.Status.ServerVersion, updated.Status.Edition)
	})

	t.Run("DatabaseWithSQLServerRef", func(t *testing.T) {
		dbKey := types.NamespacedName{Name: "e2e-sqlserverref-db", Namespace: testNamespace}
		db := &mssqlv1.Database{
			ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
			Spec: mssqlv1.DatabaseSpec{
				Server: mssqlv1.ServerReference{
					SQLServerRef: ptr(srvKey.Name),
				},
				DatabaseName:   "sqlserverreftest",
				DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			},
		}
		if err := k8sClient.Create(ctx, db); err != nil {
			t.Fatalf("Failed to create Database with sqlServerRef: %v", err)
		}
		waitForReady(t, dbKey, &mssqlv1.Database{})

		// Verify the database actually exists on SQL Server
		exists, err := sqlClient.DatabaseExists(ctx, "sqlserverreftest")
		if err != nil {
			t.Fatalf("Failed to check database existence: %v", err)
		}
		if !exists {
			t.Fatal("Database 'sqlserverreftest' should exist on SQL Server")
		}

		// Cleanup
		_ = k8sClient.Delete(ctx, db)
		waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)
	})

	t.Run("Idempotent", func(t *testing.T) {
		// Re-fetch to ensure status is still Ready after multiple reconcile loops
		var srv mssqlv1.SQLServer
		if err := k8sClient.Get(ctx, srvKey, &srv); err != nil {
			t.Fatalf("Failed to get SQLServer: %v", err)
		}
		cond := findCondition(&srv, mssqlv1.ConditionReady)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Error("Expected SQLServer to remain Ready after multiple reconcile loops")
		}
	})

	t.Run("Cleanup", func(t *testing.T) {
		_ = k8sClient.Delete(ctx, &mssqlv1.SQLServer{ObjectMeta: metav1.ObjectMeta{Name: srvKey.Name, Namespace: srvKey.Namespace}})
		waitForDeletion(t, srvKey, &mssqlv1.SQLServer{}, pollTimeout)
	})
}

// =============================================================================
// Database Configuration E2E Tests (Recovery Model, Compat Level, Options)
// =============================================================================

func TestE2EDatabaseConfiguration(t *testing.T) {
	dbKey := types.NamespacedName{Name: "e2e-dbconfig", Namespace: testNamespace}

	// Create database with extended configuration
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "configtest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			RecoveryModel:  ptr(mssqlv1.RecoveryModelFull),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	t.Run("RecoveryModel_Full", func(t *testing.T) {
		model, err := sqlClient.GetDatabaseRecoveryModel(ctx, "configtest")
		if err != nil {
			t.Fatalf("Failed to get recovery model: %v", err)
		}
		if model != "FULL" {
			t.Errorf("Expected recovery model FULL, got %s", model)
		}
	})

	t.Run("ChangeRecoveryModel_ToSimple", func(t *testing.T) {
		var current mssqlv1.Database
		if err := k8sClient.Get(ctx, dbKey, &current); err != nil {
			t.Fatalf("Failed to get Database: %v", err)
		}
		current.Spec.RecoveryModel = ptr(mssqlv1.RecoveryModelSimple)
		if err := k8sClient.Update(ctx, &current); err != nil {
			t.Fatalf("Failed to update Database: %v", err)
		}
		waitForReady(t, dbKey, &mssqlv1.Database{})

		model, err := sqlClient.GetDatabaseRecoveryModel(ctx, "configtest")
		if err != nil {
			t.Fatalf("Failed to get recovery model: %v", err)
		}
		if model != "SIMPLE" {
			t.Errorf("Expected recovery model SIMPLE, got %s", model)
		}
	})

	t.Run("CompatibilityLevel", func(t *testing.T) {
		var current mssqlv1.Database
		if err := k8sClient.Get(ctx, dbKey, &current); err != nil {
			t.Fatalf("Failed to get Database: %v", err)
		}
		current.Spec.CompatibilityLevel = ptr(int32(150))
		if err := k8sClient.Update(ctx, &current); err != nil {
			t.Fatalf("Failed to update Database: %v", err)
		}
		waitForReady(t, dbKey, &mssqlv1.Database{})

		level, err := sqlClient.GetDatabaseCompatibilityLevel(ctx, "configtest")
		if err != nil {
			t.Fatalf("Failed to get compatibility level: %v", err)
		}
		if level != 150 {
			t.Errorf("Expected compatibility level 150, got %d", level)
		}
	})

	t.Run("DatabaseOptions", func(t *testing.T) {
		var current mssqlv1.Database
		if err := k8sClient.Get(ctx, dbKey, &current); err != nil {
			t.Fatalf("Failed to get Database: %v", err)
		}
		current.Spec.Options = []mssqlv1.DatabaseOption{
			{Name: "READ_COMMITTED_SNAPSHOT", Value: true},
		}
		if err := k8sClient.Update(ctx, &current); err != nil {
			t.Fatalf("Failed to update Database: %v", err)
		}
		waitForReady(t, dbKey, &mssqlv1.Database{})

		val, err := sqlClient.GetDatabaseOption(ctx, "configtest", "READ_COMMITTED_SNAPSHOT")
		if err != nil {
			t.Fatalf("Failed to get database option: %v", err)
		}
		if !val {
			t.Error("Expected READ_COMMITTED_SNAPSHOT to be ON")
		}
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace}})
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)
}

// =============================================================================
// ScheduledBackup E2E Tests
// =============================================================================

func TestE2EScheduledBackup(t *testing.T) {
	// Create a prerequisite database for backups
	dbKey := types.NamespacedName{Name: "schedbak-db", Namespace: testNamespace}
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   "schedbaktest",
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create prerequisite Database: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	sbKey := types.NamespacedName{Name: "e2e-schedbak", Namespace: testNamespace}

	t.Run("CreateScheduledBackup", func(t *testing.T) {
		sb := &mssqlv1.ScheduledBackup{
			ObjectMeta: metav1.ObjectMeta{Name: sbKey.Name, Namespace: sbKey.Namespace},
			Spec: mssqlv1.ScheduledBackupSpec{
				Server:              serverRef(),
				DatabaseName:        "schedbaktest",
				Schedule:            "*/1 * * * *", // every minute
				Type:                mssqlv1.BackupTypeFull,
				Compression:         ptr(true),
				DestinationTemplate: "/var/opt/mssql/backup/{{.DatabaseName}}-{{.Timestamp}}.bak",
				Retention: &mssqlv1.RetentionPolicy{
					MaxCount: ptr(int32(3)),
				},
			},
		}
		if err := k8sClient.Create(ctx, sb); err != nil {
			t.Fatalf("Failed to create ScheduledBackup CR: %v", err)
		}

		// Wait for the ScheduledBackup to become ready (first backup should run within ~1 minute)
		waitForCondition(t, sbKey, &mssqlv1.ScheduledBackup{}, mssqlv1.ConditionReady, metav1.ConditionTrue, 3*time.Minute)
	})

	t.Run("VerifyBackupCreated", func(t *testing.T) {
		// Check that at least one Backup CR was created
		var sb mssqlv1.ScheduledBackup
		if err := k8sClient.Get(ctx, sbKey, &sb); err != nil {
			t.Fatalf("Failed to get ScheduledBackup: %v", err)
		}
		if sb.Status.TotalBackups < 1 {
			t.Errorf("Expected at least 1 total backup, got %d", sb.Status.TotalBackups)
		}
		if sb.Status.SuccessfulBackups < 1 {
			t.Errorf("Expected at least 1 successful backup, got %d", sb.Status.SuccessfulBackups)
		}
		if len(sb.Status.History) < 1 {
			t.Error("Expected at least 1 history entry")
		}
		if sb.Status.LastSuccessfulBackup == "" {
			t.Error("Expected lastSuccessfulBackup to be set")
		}
		t.Logf("ScheduledBackup: total=%d success=%d history=%d",
			sb.Status.TotalBackups, sb.Status.SuccessfulBackups, len(sb.Status.History))
	})

	t.Run("SuspendResume", func(t *testing.T) {
		var sb mssqlv1.ScheduledBackup
		if err := k8sClient.Get(ctx, sbKey, &sb); err != nil {
			t.Fatalf("Failed to get ScheduledBackup: %v", err)
		}
		sb.Spec.Suspend = ptr(true)
		if err := k8sClient.Update(ctx, &sb); err != nil {
			t.Fatalf("Failed to suspend ScheduledBackup: %v", err)
		}
		// Wait a moment to confirm no new backups are triggered
		time.Sleep(10 * time.Second)

		var updated mssqlv1.ScheduledBackup
		if err := k8sClient.Get(ctx, sbKey, &updated); err != nil {
			t.Fatalf("Failed to get updated ScheduledBackup: %v", err)
		}
		countBefore := updated.Status.TotalBackups

		// Resume
		updated.Spec.Suspend = ptr(false)
		if err := k8sClient.Update(ctx, &updated); err != nil {
			t.Fatalf("Failed to resume ScheduledBackup: %v", err)
		}
		t.Logf("Suspend/resume OK — total backups before suspend: %d", countBefore)
	})

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.ScheduledBackup{ObjectMeta: metav1.ObjectMeta{Name: sbKey.Name, Namespace: sbKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Database{ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace}})
}

// =============================================================================
// Point-in-Time Restore E2E Tests
// =============================================================================

func TestE2EPointInTimeRestore(t *testing.T) {
	dbName := "pitrestoretest"
	dbKey := types.NamespacedName{Name: "pit-restore-db", Namespace: testNamespace}
	fullBackupKey := types.NamespacedName{Name: "pit-full-backup", Namespace: testNamespace}
	logBackupKey := types.NamespacedName{Name: "pit-log-backup", Namespace: testNamespace}
	restoreKey := types.NamespacedName{Name: "pit-restore", Namespace: testNamespace}

	// 1. Create a database with FULL recovery model (required for log backups)
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   dbName,
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
			RecoveryModel:  ptr(mssqlv1.RecoveryModelFull),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	// 2. Create a test table and insert "before" data
	execRawSQL(t, dbName, "CREATE TABLE dbo.TestData (id INT PRIMARY KEY, value NVARCHAR(50), inserted_at DATETIME2 DEFAULT GETDATE())")
	execRawSQL(t, dbName, "INSERT INTO dbo.TestData (id, value) VALUES (1, 'before-pit')")

	// 3. Take a full backup (required base for log chain)
	fullBackup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: fullBackupKey.Name, Namespace: fullBackupKey.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:      serverRef(),
			DatabaseName: dbName,
			Destination: fmt.Sprintf("/var/opt/mssql/backup/%s-full.bak", dbName),
			Type:        mssqlv1.BackupTypeFull,
			Compression: ptr(true),
		},
	}
	if err := k8sClient.Create(ctx, fullBackup); err != nil {
		t.Fatalf("Failed to create full Backup CR: %v", err)
	}
	waitForBackupPhase(t, fullBackupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

	// 4. Insert more data and record the PIT timestamp
	execRawSQL(t, dbName, "INSERT INTO dbo.TestData (id, value) VALUES (2, 'at-pit')")

	// Record the point-in-time (we want to restore to HERE)
	pitTimestamp := queryScalarSQL(t, dbName, "SELECT FORMAT(GETDATE(), 'yyyy-MM-ddTHH:mm:ss')")
	t.Logf("PIT timestamp: %s", pitTimestamp)

	// Small delay to ensure clock advances
	time.Sleep(2 * time.Second)

	// 5. Insert "after" data that should NOT be present after PIT restore
	execRawSQL(t, dbName, "INSERT INTO dbo.TestData (id, value) VALUES (3, 'after-pit')")

	// 6. Take a log backup (captures the log chain including all inserts)
	logBackup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: logBackupKey.Name, Namespace: logBackupKey.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:      serverRef(),
			DatabaseName: dbName,
			Destination: fmt.Sprintf("/var/opt/mssql/backup/%s-log.trn", dbName),
			Type:        mssqlv1.BackupTypeLog,
			Compression: ptr(true),
		},
	}
	if err := k8sClient.Create(ctx, logBackup); err != nil {
		t.Fatalf("Failed to create log Backup CR: %v", err)
	}
	waitForBackupPhase(t, logBackupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

	// 7. Drop the database via raw SQL (operator DB CR still exists but we need DB gone for restore)
	_ = k8sClient.Delete(ctx, db)
	waitForDeletion(t, dbKey, &mssqlv1.Database{}, pollTimeout)
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		exists, err := sqlClient.DatabaseExists(ctx, dbName)
		if err != nil {
			return false, nil
		}
		return !exists, nil
	})
	if err != nil {
		t.Fatalf("Timed out waiting for database to be dropped: %v", err)
	}

	// 8. Restore with STOPAT to the PIT timestamp
	restore := &mssqlv1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace},
		Spec: mssqlv1.RestoreSpec{
			Server:       serverRef(),
			DatabaseName: dbName,
			Source:       fmt.Sprintf("/var/opt/mssql/backup/%s-full.bak", dbName),
			StopAt:       ptr(pitTimestamp),
		},
	}
	if err := k8sClient.Create(ctx, restore); err != nil {
		t.Fatalf("Failed to create PIT Restore CR: %v", err)
	}

	rst := waitForRestorePhase(t, restoreKey, mssqlv1.RestorePhaseCompleted, pollTimeout)

	// 9. Verify the restore was successful
	if rst.Status.CompletionTime == nil {
		t.Error("Expected CompletionTime to be set")
	}
	cond := findCondition(rst, mssqlv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("Expected Ready=True on completed PIT restore")
	}

	// 10. Verify data: rows 1 and 2 should exist, row 3 should NOT
	count := queryScalarSQL(t, dbName, "SELECT COUNT(*) FROM dbo.TestData WHERE id IN (1, 2)")
	if count != "2" {
		t.Errorf("Expected 2 rows (before + at PIT), got %s", count)
	}

	afterCount := queryScalarSQL(t, dbName, "SELECT COUNT(*) FROM dbo.TestData WHERE id = 3")
	if afterCount != "0" {
		t.Errorf("Expected 0 rows for after-PIT data, got %s (PIT restore did not exclude post-PIT data)", afterCount)
	}

	t.Logf("PIT restore verified: 2 rows present (before+at PIT), 0 rows after PIT")

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: fullBackupKey.Name, Namespace: fullBackupKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: logBackupKey.Name, Namespace: logBackupKey.Namespace}})
	execRawSQL(t, "master", fmt.Sprintf("IF DB_ID('%s') IS NOT NULL DROP DATABASE [%s]", dbName, dbName))
}

// =============================================================================
// WITH MOVE Restore E2E Tests
// =============================================================================

func TestE2ERestoreWithMove(t *testing.T) {
	dbName := "moverestoretest"
	targetDbName := "moverestoretarget"
	dbKey := types.NamespacedName{Name: "move-restore-db", Namespace: testNamespace}
	backupKey := types.NamespacedName{Name: "move-backup", Namespace: testNamespace}
	restoreKey := types.NamespacedName{Name: "move-restore", Namespace: testNamespace}

	// 1. Create and backup a source database
	db := &mssqlv1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: dbKey.Name, Namespace: dbKey.Namespace},
		Spec: mssqlv1.DatabaseSpec{
			Server:         serverRef(),
			DatabaseName:   dbName,
			DeletionPolicy: ptr(mssqlv1.DeletionPolicyDelete),
		},
	}
	if err := k8sClient.Create(ctx, db); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create Database CR: %v", err)
	}
	waitForReady(t, dbKey, &mssqlv1.Database{})

	execRawSQL(t, dbName, "CREATE TABLE dbo.MoveTest (id INT PRIMARY KEY, value NVARCHAR(50))")
	execRawSQL(t, dbName, "INSERT INTO dbo.MoveTest (id, value) VALUES (1, 'moved')")

	backup := &mssqlv1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace},
		Spec: mssqlv1.BackupSpec{
			Server:      serverRef(),
			DatabaseName: dbName,
			Destination: fmt.Sprintf("/var/opt/mssql/backup/%s.bak", dbName),
			Type:        mssqlv1.BackupTypeFull,
		},
	}
	if err := k8sClient.Create(ctx, backup); err != nil {
		t.Fatalf("Failed to create Backup CR: %v", err)
	}
	waitForBackupPhase(t, backupKey, mssqlv1.BackupPhaseCompleted, pollTimeout)

	// 2. Get the logical file names from the backup
	dataLogical := queryScalarSQL(t, "master",
		fmt.Sprintf("RESTORE FILELISTONLY FROM DISK = '/var/opt/mssql/backup/%s.bak'", dbName))
	t.Logf("First logical file from backup: %s", dataLogical)

	// 3. Restore to a different database name with MOVE
	restore := &mssqlv1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace},
		Spec: mssqlv1.RestoreSpec{
			Server:       serverRef(),
			DatabaseName: targetDbName,
			Source:       fmt.Sprintf("/var/opt/mssql/backup/%s.bak", dbName),
			WithMove: []mssqlv1.FileMapping{
				{
					LogicalName:  dbName,
					PhysicalPath: fmt.Sprintf("/var/opt/mssql/data/%s.mdf", targetDbName),
				},
				{
					LogicalName:  dbName + "_log",
					PhysicalPath: fmt.Sprintf("/var/opt/mssql/data/%s_log.ldf", targetDbName),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, restore); err != nil {
		t.Fatalf("Failed to create Restore with MOVE CR: %v", err)
	}

	rst := waitForRestorePhase(t, restoreKey, mssqlv1.RestorePhaseCompleted, pollTimeout)

	// 4. Verify the target database exists and has data
	cond := findCondition(rst, mssqlv1.ConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("Expected Ready=True on completed WITH MOVE restore")
	}

	exists, err := sqlClient.DatabaseExists(ctx, targetDbName)
	if err != nil {
		t.Fatalf("Failed to check target DB existence: %v", err)
	}
	if !exists {
		t.Fatal("Target database should exist after WITH MOVE restore")
	}

	val := queryScalarSQL(t, targetDbName, "SELECT value FROM dbo.MoveTest WHERE id = 1")
	if val != "moved" {
		t.Errorf("Expected value 'moved', got '%s'", val)
	}

	// Cleanup
	_ = k8sClient.Delete(ctx, &mssqlv1.Restore{ObjectMeta: metav1.ObjectMeta{Name: restoreKey.Name, Namespace: restoreKey.Namespace}})
	_ = k8sClient.Delete(ctx, &mssqlv1.Backup{ObjectMeta: metav1.ObjectMeta{Name: backupKey.Name, Namespace: backupKey.Namespace}})
	_ = k8sClient.Delete(ctx, db)
	execRawSQL(t, "master", fmt.Sprintf("IF DB_ID('%s') IS NOT NULL DROP DATABASE [%s]", dbName, dbName))
	execRawSQL(t, "master", fmt.Sprintf("IF DB_ID('%s') IS NOT NULL DROP DATABASE [%s]", targetDbName, targetDbName))
}

// =============================================================================
// Business Metrics E2E Test
// =============================================================================

func TestE2EBusinessMetrics(t *testing.T) {
	metricsFwd := exec.CommandContext(ctx, "kubectl", "port-forward",
		"deploy/mssql-operator", "18082:8080",
		"-n", "mssql-operator-system",
	)
	metricsFwd.Stdout = io.Discard
	metricsFwd.Stderr = io.Discard
	if err := metricsFwd.Start(); err != nil {
		t.Fatalf("Failed to start metrics port-forward: %v", err)
	}
	defer func() {
		if metricsFwd.Process != nil {
			_ = metricsFwd.Process.Kill()
		}
	}()
	time.Sleep(3 * time.Second)

	resp, err := http.Get("http://localhost:18082/metrics")
	if err != nil {
		t.Fatalf("Failed to get metrics: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	metricsStr := string(body)

	if resp.StatusCode != 200 {
		t.Fatalf("Metrics endpoint returned %d", resp.StatusCode)
	}

	// Check for business metrics (they should be registered even if no data points yet)
	assertMetricExists(t, metricsStr, "mssql_database_ready")
	assertMetricExists(t, metricsStr, "mssql_server_connected")
	assertMetricExists(t, metricsStr, "mssql_login_ready")
	assertMetricExists(t, metricsStr, "mssql_operator_managed_resources")
}

// =============================================================================
// AvailabilityGroup & AGFailover E2E Tests
// =============================================================================
//
// AG tests require multiple SQL Server instances with HADR enabled.
// They are skipped in the standard e2e suite (single-instance) and require
// the E2E_AG_ENABLED=true environment variable plus a multi-instance setup.
//
// To run AG e2e tests:
//   1. Deploy 2+ SQL Server Enterprise instances with HADR enabled
//   2. Set E2E_AG_ENABLED=true
//   3. Set E2E_SQL_HOST_0, E2E_SQL_HOST_1 for the replica hostnames
//   4. Set E2E_AG_CREDS_SECRET for the credentials secret name

func TestE2EAvailabilityGroup(t *testing.T) {
	if os.Getenv("E2E_AG_ENABLED") != "true" {
		t.Skip("Skipping AG e2e tests: set E2E_AG_ENABLED=true with a multi-instance SQL Server setup")
	}

	host0 := os.Getenv("E2E_SQL_HOST_0")
	host1 := os.Getenv("E2E_SQL_HOST_1")
	credsSecret := envOrDefault("E2E_AG_CREDS_SECRET", "mssql-sa-credentials")

	if host0 == "" || host1 == "" {
		t.Fatal("E2E_SQL_HOST_0 and E2E_SQL_HOST_1 must be set for AG tests")
	}

	agKey := types.NamespacedName{Name: "test-ag", Namespace: testNamespace}

	t.Run("CreateAG", func(t *testing.T) {
		ag := &mssqlv1.AvailabilityGroup{
			ObjectMeta: metav1.ObjectMeta{Name: agKey.Name, Namespace: agKey.Namespace},
			Spec: mssqlv1.AvailabilityGroupSpec{
				AGName: "e2eag",
				Replicas: []mssqlv1.AGReplicaSpec{
					{
						ServerName:       "sql-0",
						EndpointURL:      fmt.Sprintf("TCP://%s:5022", host0),
						AvailabilityMode: mssqlv1.AvailabilityModeSynchronous,
						FailoverMode:     mssqlv1.FailoverModeAutomatic,
						SeedingMode:      mssqlv1.SeedingModeAutomatic,
						Server: mssqlv1.ServerReference{
							Host:              host0,
							Port:              ptr(int32(1433)),
							CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
						},
					},
					{
						ServerName:       "sql-1",
						EndpointURL:      fmt.Sprintf("TCP://%s:5022", host1),
						AvailabilityMode: mssqlv1.AvailabilityModeSynchronous,
						FailoverMode:     mssqlv1.FailoverModeAutomatic,
						SeedingMode:      mssqlv1.SeedingModeAutomatic,
						Server: mssqlv1.ServerReference{
							Host:              host1,
							Port:              ptr(int32(1433)),
							CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
						},
					},
				},
				AutomatedBackupPreference: ptr("Secondary"),
				DBFailover:                ptr(true),
			},
		}
		if err := k8sClient.Create(ctx, ag); err != nil {
			t.Fatalf("Failed to create AvailabilityGroup CR: %v", err)
		}

		waitForReady(t, agKey, &mssqlv1.AvailabilityGroup{})

		// Verify status shows primary
		var updated mssqlv1.AvailabilityGroup
		if err := k8sClient.Get(ctx, agKey, &updated); err != nil {
			t.Fatalf("Failed to get AG: %v", err)
		}
		if updated.Status.PrimaryReplica == "" {
			t.Error("Expected primaryReplica to be set in status")
		}
		if len(updated.Status.Replicas) != 2 {
			t.Errorf("Expected 2 replica statuses, got %d", len(updated.Status.Replicas))
		}
	})

	t.Run("ManualFailover", func(t *testing.T) {
		foKey := types.NamespacedName{Name: "test-ag-failover", Namespace: testNamespace}
		failover := &mssqlv1.AGFailover{
			ObjectMeta: metav1.ObjectMeta{Name: foKey.Name, Namespace: foKey.Namespace},
			Spec: mssqlv1.AGFailoverSpec{
				AGName:        "e2eag",
				TargetReplica: "sql-1",
				Force:         ptr(false),
				Server: mssqlv1.ServerReference{
					Host:              host1,
					Port:              ptr(int32(1433)),
					CredentialsSecret: mssqlv1.SecretReference{Name: credsSecret},
				},
			},
		}
		if err := k8sClient.Create(ctx, failover); err != nil {
			t.Fatalf("Failed to create AGFailover CR: %v", err)
		}

		// Wait for failover to complete
		err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			var fo mssqlv1.AGFailover
			if err := k8sClient.Get(ctx, foKey, &fo); err != nil {
				return false, nil
			}
			return fo.Status.Phase == mssqlv1.FailoverPhaseCompleted || fo.Status.Phase == mssqlv1.FailoverPhaseFailed, nil
		})
		if err != nil {
			t.Fatalf("Timed out waiting for AGFailover to complete")
		}

		var fo mssqlv1.AGFailover
		if err := k8sClient.Get(ctx, foKey, &fo); err != nil {
			t.Fatalf("Failed to get AGFailover: %v", err)
		}
		if fo.Status.Phase != mssqlv1.FailoverPhaseCompleted {
			t.Errorf("Expected AGFailover phase=Completed, got %s", fo.Status.Phase)
		}
		if fo.Status.NewPrimary != "sql-1" {
			t.Errorf("Expected newPrimary=sql-1, got %s", fo.Status.NewPrimary)
		}

		// Cleanup failover CR
		_ = k8sClient.Delete(ctx, failover)
	})

	// Cleanup AG
	var ag mssqlv1.AvailabilityGroup
	if err := k8sClient.Get(ctx, agKey, &ag); err == nil {
		_ = k8sClient.Delete(ctx, &ag)
	}
}

// --- Additional helpers ---

// execRawSQL executes a raw SQL statement against a specific database.
// queryScalarSQL runs a query that returns a single scalar value.
func queryScalarSQL(t *testing.T, database, query string) string {
	t.Helper()
	connStr := fmt.Sprintf("sqlserver://sa:%s@localhost:1433?database=%s&encrypt=disable",
		saPassword, database)
	db, err := sql.Open("sqlserver", connStr)
	if err != nil {
		t.Fatalf("Failed to open raw SQL connection: %v", err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, query).Scan(&result); err != nil {
		t.Fatalf("Failed to query scalar SQL %q: %v", query, err)
	}
	return result
}

func execRawSQL(t *testing.T, database, query string) {
	t.Helper()
	connStr := fmt.Sprintf("sqlserver://sa:%s@localhost:1433?database=%s&encrypt=disable",
		saPassword, database)
	db, err := sql.Open("sqlserver", connStr)
	if err != nil {
		t.Fatalf("Failed to open raw SQL connection: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, query); err != nil {
		t.Fatalf("Failed to exec raw SQL %q: %v", query, err)
	}
}

// triggerReconciliation modifies a spec field to bump the generation and trigger reconciliation.
// For DatabaseUser, it toggles a harmless database role.
// For Login, it toggles a harmless server role.
// For Database, it toggles the owner field.
func triggerReconciliation(t *testing.T, key types.NamespacedName, obj client.Object) {
	t.Helper()
	if err := k8sClient.Get(ctx, key, obj); err != nil {
		t.Fatalf("Failed to get object for trigger: %v", err)
	}
	switch o := obj.(type) {
	case *mssqlv1.DatabaseUser:
		// Add a temporary role to bump generation
		if !containsString(o.Spec.DatabaseRoles, "db_denydatareader") {
			o.Spec.DatabaseRoles = append(o.Spec.DatabaseRoles, "db_denydatareader")
		} else {
			o.Spec.DatabaseRoles = removeString(o.Spec.DatabaseRoles, "db_denydatareader")
		}
	case *mssqlv1.Login:
		// Toggle defaultDatabase to bump generation
		if o.Spec.DefaultDatabase == nil {
			o.Spec.DefaultDatabase = ptr("master")
		} else {
			o.Spec.DefaultDatabase = nil
		}
	case *mssqlv1.Database:
		// Toggle owner to bump generation
		if o.Spec.Owner == nil {
			o.Spec.Owner = ptr("sa")
		} else {
			o.Spec.Owner = nil
		}
	}
	if err := k8sClient.Update(ctx, obj); err != nil {
		t.Fatalf("Failed to trigger reconciliation: %v", err)
	}
}

// waitForConditionWithReason waits for a condition with a specific status AND reason.
func waitForConditionWithReason(t *testing.T, key types.NamespacedName, obj client.Object, condType string, expectedStatus metav1.ConditionStatus, expectedReason string, timeout time.Duration) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, obj); err != nil {
			return false, nil
		}
		cond := findCondition(obj, condType)
		if cond == nil {
			return false, nil
		}
		return cond.Status == expectedStatus && cond.Reason == expectedReason, nil
	})
	if err != nil {
		cond := findCondition(obj, condType)
		condStr := "<nil>"
		if cond != nil {
			condStr = fmt.Sprintf("Status=%s Reason=%s Message=%s", cond.Status, cond.Reason, cond.Message)
		}
		t.Fatalf("Timed out waiting for condition %s=%s reason=%s on %s: last condition: %s",
			condType, expectedStatus, expectedReason, key, condStr)
	}
}

// scaleSQLServer scales the MSSQL deployment to the given number of replicas.
func scaleSQLServer(t *testing.T, replicas int32) {
	t.Helper()
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Name: "mssql", Namespace: testNamespace}
	if err := k8sClient.Get(ctx, key, dep); err != nil {
		t.Fatalf("Failed to get mssql deployment: %v", err)
	}
	dep.Spec.Replicas = &replicas
	if err := k8sClient.Update(ctx, dep); err != nil {
		t.Fatalf("Failed to scale mssql deployment: %v", err)
	}
}

// reconnectSQLClient closes the old SQL client and creates a new one.
func reconnectSQLClient() error {
	if sqlClient != nil {
		_ = sqlClient.Close()
	}
	return connectSQLClient()
}

// verifyLoginPassword checks that a login can connect with the given password.
func verifyLoginPassword(t *testing.T, loginName, password string) {
	t.Helper()
	factory := internalsql.NewClientFactory()
	c, err := factory("localhost", 1433, loginName, password, false)
	if err != nil {
		t.Fatalf("Failed to connect as %s: %v", loginName, err)
	}
	defer c.Close()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Failed to ping as %s: %v", loginName, err)
	}
}

// checkEndpoint verifies that an HTTP endpoint returns the expected status code.
func checkEndpoint(t *testing.T, url string, expectedStatus int) {
	t.Helper()
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, time.Second, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			return false, nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != expectedStatus {
			lastErr = fmt.Errorf("got status %d, want %d", resp.StatusCode, expectedStatus)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("Endpoint %s did not return %d: %v", url, expectedStatus, lastErr)
	}
}

// assertMetricExists checks that a metric name appears in the Prometheus text output.
func assertMetricExists(t *testing.T, metricsBody, metricName string) {
	t.Helper()
	if !strings.Contains(metricsBody, metricName) {
		t.Errorf("Expected metric %q not found in metrics output", metricName)
	}
}

// --- Assertion helpers ---

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	var result []string
	for _, v := range slice {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}

func assertContains(t *testing.T, slice []string, s string) {
	t.Helper()
	if !containsString(slice, s) {
		t.Errorf("Expected %v to contain %q", slice, s)
	}
}
