//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"fmt"
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
	defaultNamespace     = "e2e-test"
	defaultSAPassword    = "P@ssw0rd123!"
	defaultSQLImage      = "mcr.microsoft.com/mssql/server:2022-latest"
	defaultOperatorImage = "mssql-k8s-operator:latest"

	pollInterval = 2 * time.Second
	pollTimeout  = 120 * time.Second

	helmReleaseName = "mssql-operator"
	helmChartPath   = "../../charts/mssql-operator"
)

var (
	k8sClient     client.Client
	sqlClient     internalsql.SQLClient
	ctx           context.Context
	cancel        context.CancelFunc
	portFwdCmd    *exec.Cmd
	testNamespace string
	saPassword    string
	sqlImage      string
	operatorImage string
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

// --- AG Infrastructure helpers ---

func deployAGInfrastructure(t *testing.T) {
	t.Helper()

	// Headless service for inter-pod DNS resolution
	headlessSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "sql-headless", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  map[string]string{"app": "mssql-ag"},
			Ports: []corev1.ServicePort{
				{Name: "sql", Port: 1433, TargetPort: intstr.FromInt32(1433)},
				{Name: "hadr", Port: 5022, TargetPort: intstr.FromInt32(5022)},
			},
		},
	}
	if err := createOrUpdate(headlessSvc); err != nil {
		t.Fatalf("Failed to create headless service: %v", err)
	}

	// StatefulSet with 2 SQL Server replicas, HADR enabled
	labels := map[string]string{"app": "mssql-ag"}
	replicas := int32(2)
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "sql", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "sql-headless",
			Replicas:    &replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
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
							{Name: "MSSQL_ENABLE_HADR", Value: "1"},
						},
						Ports: []corev1.ContainerPort{
							{ContainerPort: 1433, Name: "sql"},
							{ContainerPort: 5022, Name: "hadr"},
						},
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
								TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(1433)},
							},
							InitialDelaySeconds: 20,
							PeriodSeconds:       10,
						},
					}},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, ss); err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Failed to create StatefulSet: %v", err)
	}

	// Wait for both pods to be ready
	t.Log("Waiting for AG SQL Server pods to be ready...")
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		var sts appsv1.StatefulSet
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "sql", Namespace: testNamespace}, &sts); err != nil {
			return false, nil
		}
		return sts.Status.ReadyReplicas >= 2, nil
	})
	if err != nil {
		t.Fatalf("AG SQL Server pods did not become ready: %v", err)
	}

	// Wait for SQL Server to accept connections on both pods
	for _, pod := range []string{"sql-0", "sql-1"} {
		t.Logf("Waiting for %s to accept connections...", pod)
		err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
			cmd := exec.CommandContext(ctx, "kubectl", "exec", pod, "-n", testNamespace,
				"--", "/opt/mssql-tools18/bin/sqlcmd",
				"-S", "localhost", "-U", "sa", "-P", saPassword,
				"-Q", "SELECT 1", "-C", "-No")
			return cmd.Run() == nil, nil
		})
		if err != nil {
			t.Fatalf("Pod %s did not accept SQL connections: %v", pod, err)
		}
	}
	t.Log("AG SQL Server infrastructure ready")
}

func setupAGCertificates(t *testing.T) {
	t.Helper()
	t.Log("Setting up AG certificates and endpoints...")

	pods := []string{"sql-0", "sql-1"}

	// 1. Create master keys and certificates on each pod
	for i, pod := range pods {
		certName := fmt.Sprintf("ag_cert_%d", i)
		certFile := fmt.Sprintf("/var/opt/mssql/backup/%s.cer", certName)
		keyFile := fmt.Sprintf("/var/opt/mssql/backup/%s.key", certName)

		queries := []string{
			"IF NOT EXISTS (SELECT 1 FROM sys.symmetric_keys WHERE name = '##MS_DatabaseMasterKey##') CREATE MASTER KEY ENCRYPTION BY PASSWORD = 'MasterKeyP@ss1!'",
			fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.certificates WHERE name = '%s') CREATE CERTIFICATE %s WITH SUBJECT = '%s certificate', EXPIRY_DATE = '2030-01-01'", certName, certName, pod),
			fmt.Sprintf("BACKUP CERTIFICATE %s TO FILE = '%s' WITH PRIVATE KEY (FILE = '%s', ENCRYPTION BY PASSWORD = 'CertP@ss123!')", certName, certFile, keyFile),
		}
		for _, q := range queries {
			execSQLOnPod(t, pod, q)
		}
	}

	// 2. Copy certificates between pods
	tmpDir := t.TempDir()
	for i, srcPod := range pods {
		dstPod := pods[1-i]
		certName := fmt.Sprintf("ag_cert_%d", i)
		certFile := fmt.Sprintf("%s.cer", certName)
		keyFile := fmt.Sprintf("%s.key", certName)

		// Copy cert+key from srcPod to local
		for _, f := range []string{certFile, keyFile} {
			cmd := exec.CommandContext(ctx, "kubectl", "cp",
				fmt.Sprintf("%s/%s:/var/opt/mssql/backup/%s", testNamespace, srcPod, f),
				fmt.Sprintf("%s/%s", tmpDir, f))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Failed to cp %s from %s: %v: %s", f, srcPod, err, out)
			}
		}
		// Copy cert+key from local to dstPod
		for _, f := range []string{certFile, keyFile} {
			cmd := exec.CommandContext(ctx, "kubectl", "cp",
				fmt.Sprintf("%s/%s", tmpDir, f),
				fmt.Sprintf("%s/%s:/var/opt/mssql/backup/%s", testNamespace, dstPod, f))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Failed to cp %s to %s: %v: %s", f, dstPod, err, out)
			}
		}
	}

	// 3. Import peer certificates, create logins, create endpoints
	for i, pod := range pods {
		peerIdx := 1 - i
		peerCertName := fmt.Sprintf("ag_cert_%d", peerIdx)
		localCertName := fmt.Sprintf("ag_cert_%d", i)
		peerCertFile := fmt.Sprintf("/var/opt/mssql/backup/%s.cer", peerCertName)
		peerKeyFile := fmt.Sprintf("/var/opt/mssql/backup/%s.key", peerCertName)
		loginName := fmt.Sprintf("ag_login_%d", peerIdx)

		queries := []string{
			// Import peer certificate
			fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.certificates WHERE name = '%s') CREATE CERTIFICATE %s FROM FILE = '%s' WITH PRIVATE KEY (FILE = '%s', DECRYPTION BY PASSWORD = 'CertP@ss123!')", peerCertName, peerCertName, peerCertFile, peerKeyFile),
			// Create login from peer cert
			fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.server_principals WHERE name = '%s') CREATE LOGIN %s FROM CERTIFICATE %s", loginName, loginName, peerCertName),
			// Create endpoint with local cert auth
			fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.database_mirroring_endpoints) CREATE ENDPOINT hadr_endpoint STATE = STARTED AS TCP (LISTENER_PORT = 5022) FOR DATABASE_MIRRORING (ROLE = ALL, AUTHENTICATION = CERTIFICATE %s, ENCRYPTION = DISABLED)", localCertName),
			// Grant connect on endpoint to peer login
			fmt.Sprintf("GRANT CONNECT ON ENDPOINT::hadr_endpoint TO %s", loginName),
		}
		for _, q := range queries {
			execSQLOnPod(t, pod, q)
		}
	}
	t.Log("AG certificates and endpoints configured")
}

// setupAGCertificatesForPods sets up HADR certificate exchange between pods.
// This handles the cert backup, copy, import, and endpoint grant for arbitrary pod names.
func setupAGCertificatesForPods(t *testing.T, pods []string) {
	t.Helper()
	t.Log("Setting up HADR certificates for managed pods...")

	// 1. Create master keys, certs, and endpoints on each pod (if not already done by controller)
	for i, pod := range pods {
		certName := fmt.Sprintf("ag_cert_%d", i)
		certFile := fmt.Sprintf("/var/opt/mssql/backup/%s.cer", certName)
		keyFile := fmt.Sprintf("/var/opt/mssql/backup/%s.key", certName)

		queries := []string{
			"IF NOT EXISTS (SELECT 1 FROM sys.symmetric_keys WHERE name = '##MS_DatabaseMasterKey##') CREATE MASTER KEY ENCRYPTION BY PASSWORD = 'MasterKeyP@ss1!'",
			fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.certificates WHERE name = '%s') CREATE CERTIFICATE %s WITH SUBJECT = '%s certificate', EXPIRY_DATE = '2030-01-01'", certName, certName, pod),
			fmt.Sprintf("IF NOT EXISTS (SELECT * FROM sys.database_mirroring_endpoints) CREATE ENDPOINT hadr_endpoint STATE = STARTED AS TCP (LISTENER_PORT = 5022) FOR DATABASE_MIRRORING (ROLE = ALL, AUTHENTICATION = CERTIFICATE %s, ENCRYPTION = DISABLED)", certName),
			fmt.Sprintf("BACKUP CERTIFICATE %s TO FILE = '%s' WITH PRIVATE KEY (FILE = '%s', ENCRYPTION BY PASSWORD = 'CertP@ss123!')", certName, certFile, keyFile),
		}
		for _, q := range queries {
			execSQLOnPod(t, pod, q)
		}
	}

	// 2. Copy certificates between pods
	tmpDir := t.TempDir()
	for i, srcPod := range pods {
		certName := fmt.Sprintf("ag_cert_%d", i)
		for _, ext := range []string{".cer", ".key"} {
			f := certName + ext
			cmd := exec.CommandContext(ctx, "kubectl", "cp",
				fmt.Sprintf("%s/%s:/var/opt/mssql/backup/%s", testNamespace, srcPod, f),
				fmt.Sprintf("%s/%s", tmpDir, f))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Failed to cp %s from %s: %v: %s", f, srcPod, err, out)
			}
		}
	}
	for i := range pods {
		for j, dstPod := range pods {
			if i == j {
				continue
			}
			certName := fmt.Sprintf("ag_cert_%d", i)
			for _, ext := range []string{".cer", ".key"} {
				f := certName + ext
				cmd := exec.CommandContext(ctx, "kubectl", "cp",
					fmt.Sprintf("%s/%s", tmpDir, f),
					fmt.Sprintf("%s/%s:/var/opt/mssql/backup/%s", testNamespace, dstPod, f))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("Failed to cp %s to %s: %v: %s", f, dstPod, err, out)
				}
			}
		}
	}

	// 3. Import peer certs and grant connect
	for i, pod := range pods {
		for j := range pods {
			if i == j {
				continue
			}
			peerCert := fmt.Sprintf("ag_cert_%d", j)
			peerLogin := fmt.Sprintf("ag_login_%d", j)
			queries := []string{
				fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.certificates WHERE name = '%s') CREATE CERTIFICATE %s FROM FILE = '/var/opt/mssql/backup/%s.cer' WITH PRIVATE KEY (FILE = '/var/opt/mssql/backup/%s.key', DECRYPTION BY PASSWORD = 'CertP@ss123!')", peerCert, peerCert, peerCert, peerCert),
				fmt.Sprintf("IF NOT EXISTS (SELECT 1 FROM sys.server_principals WHERE name = '%s') CREATE LOGIN %s FROM CERTIFICATE %s", peerLogin, peerLogin, peerCert),
				fmt.Sprintf("GRANT CONNECT ON ENDPOINT::hadr_endpoint TO %s", peerLogin),
			}
			for _, q := range queries {
				execSQLOnPod(t, pod, q)
			}
		}
	}
	t.Log("HADR certificates configured for managed pods")
}

func execSQLOnPod(t *testing.T, pod, query string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "kubectl", "exec", pod, "-n", testNamespace, "--",
		"/opt/mssql-tools18/bin/sqlcmd",
		"-S", "localhost", "-U", "sa", "-P", saPassword,
		"-Q", query, "-C", "-No")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SQL on %s failed: %v\nQuery: %s\nOutput: %s", pod, err, query, out)
	}
}

// execRawSQLIgnoreError is like execRawSQL but doesn't fail the test on error.
func execRawSQLIgnoreError(t *testing.T, database, query string) {
	t.Helper()
	connStr := fmt.Sprintf("sqlserver://sa:%s@localhost:1433?database=%s&encrypt=disable",
		saPassword, database)
	db, err := sql.Open("sqlserver", connStr)
	if err != nil {
		return
	}
	defer db.Close()
	_, _ = db.ExecContext(ctx, query)
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
