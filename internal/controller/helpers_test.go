package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// errTest is a shared test error used across all controller tests.
var errTest = errors.New("test error")

func TestGetCredentialsFromSecret_Valid(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data: map[string][]byte{
			"username": []byte("sa"),
			"password": []byte("s3cret"),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	user, pass, err := getCredentialsFromSecret(context.Background(), c, "default", "creds")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != "sa" || pass != "s3cret" {
		t.Errorf("got user=%q pass=%q", user, pass)
	}
}

func TestGetCredentialsFromSecret_NotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, _, err := getCredentialsFromSecret(context.Background(), c, "default", "missing")
	if err == nil {
		t.Error("expected error for missing secret")
	}
}

func TestGetCredentialsFromSecret_MissingKey(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data: map[string][]byte{
			"username": []byte("sa"),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	_, _, err := getCredentialsFromSecret(context.Background(), c, "default", "creds")
	if err == nil {
		t.Error("expected error for missing 'password' key")
	}
}

func TestConnectToSQL_DefaultPortAndTLS(t *testing.T) {
	var capturedHost string
	var capturedPort int
	var capturedTLS bool

	factory := func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
		capturedHost = host
		capturedPort = port
		capturedTLS = tlsEnabled
		return sqlclient.NewMockClient(), nil
	}

	server := v1alpha1.ServerReference{
		Host:              "myhost",
		CredentialsSecret: v1alpha1.SecretReference{Name: "sa"},
	}

	_, err := connectToSQL(server, "sa", "pass", factory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedHost != "myhost" {
		t.Errorf("expected host=myhost, got %s", capturedHost)
	}
	if capturedPort != 1433 {
		t.Errorf("expected default port=1433, got %d", capturedPort)
	}
	if capturedTLS != false {
		t.Error("expected TLS=false by default")
	}
}

func TestConnectToSQL_CustomPortAndTLS(t *testing.T) {
	port := int32(5000)
	tls := true
	var capturedPort int
	var capturedTLS bool

	factory := func(host string, p int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
		capturedPort = p
		capturedTLS = tlsEnabled
		return sqlclient.NewMockClient(), nil
	}

	server := v1alpha1.ServerReference{
		Host:              "myhost",
		Port:              &port,
		TLS:               &tls,
		CredentialsSecret: v1alpha1.SecretReference{Name: "sa"},
	}

	_, err := connectToSQL(server, "sa", "pass", factory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPort != 5000 {
		t.Errorf("expected port=5000, got %d", capturedPort)
	}
	if !capturedTLS {
		t.Error("expected TLS=true")
	}
}

func TestConnectToSQL_FactoryError(t *testing.T) {
	factory := func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
		return nil, fmt.Errorf("connection refused")
	}

	server := v1alpha1.ServerReference{
		Host:              "myhost",
		CredentialsSecret: v1alpha1.SecretReference{Name: "sa"},
	}

	_, err := connectToSQL(server, "sa", "pass", factory)
	if err == nil {
		t.Error("expected error from factory")
	}
}
