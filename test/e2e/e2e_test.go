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
	}
	return meta.FindStatusCondition(conditions, condType)
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

	// Check for custom metrics
	assertMetricExists(t, metricsStr, "mssql_operator_reconcile_total")
	assertMetricExists(t, metricsStr, "mssql_operator_reconcile_duration_seconds")
}

// --- Additional helpers ---

// execRawSQL executes a raw SQL statement against a specific database.
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
