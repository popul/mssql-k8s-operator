package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
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
