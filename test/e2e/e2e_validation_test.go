//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mssqlv1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

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

	// Check for business metrics registration (only metrics that have been emitted by controllers so far)
	// database_ready is emitted by the Database controller after any successful reconciliation
	assertMetricExists(t, metricsStr, "mssql_database_ready")
}
