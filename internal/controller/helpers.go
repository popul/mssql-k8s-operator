package controller

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

const (
	// sqlOperationTimeout is the maximum time for a single SQL operation.
	sqlOperationTimeout = 30 * time.Second

	// requeueInterval is the base interval between periodic reconciliation.
	requeueInterval = 30 * time.Second
)

// getCredentialsFromSecret reads "username" and "password" keys from a Kubernetes Secret.
func getCredentialsFromSecret(ctx context.Context, c client.Client, namespace, secretName string) (string, string, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &secret); err != nil {
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

// connectToSQL creates a SQL client from a ServerReference, credentials, and factory.
func connectToSQL(server v1alpha1.ServerReference, username, password string, factory sqlclient.ClientFactory) (sqlclient.SQLClient, error) {
	port := int32(1433)
	if server.Port != nil {
		port = *server.Port
	}
	tlsEnabled := false
	if server.TLS != nil {
		tlsEnabled = *server.TLS
	}
	return factory(server.Host, int(port), username, password, tlsEnabled)
}

// sqlContext returns a child context with a timeout suitable for SQL operations.
func sqlContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, sqlOperationTimeout)
}

// toSet converts a string slice to a map for O(1) lookups.
func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

// requeueWithJitter returns a RequeueAfter duration with ±20% jitter to avoid thundering herd.
func requeueWithJitter(base time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(base*2/5))) - base/5
	return base + jitter
}
