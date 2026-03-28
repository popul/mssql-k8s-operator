package controller

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

// mapSecretToDatabases returns reconcile requests for all Database CRs that reference the given Secret.
func mapSecretToDatabases(ctx context.Context, c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.DatabaseList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, db := range list.Items {
			if db.Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: db.Name, Namespace: db.Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToLogins returns reconcile requests for all Login CRs that reference the given Secret.
func mapSecretToLogins(ctx context.Context, c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.LoginList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, login := range list.Items {
			if login.Spec.Server.CredentialsSecret.Name == obj.GetName() ||
				login.Spec.PasswordSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: login.Name, Namespace: login.Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToDatabaseUsers returns reconcile requests for all DatabaseUser CRs that reference the given Secret.
func mapSecretToDatabaseUsers(ctx context.Context, c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.DatabaseUserList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, user := range list.Items {
			if user.Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: user.Name, Namespace: user.Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToSchemas returns reconcile requests for all Schema CRs that reference the given Secret.
func mapSecretToSchemas(ctx context.Context, c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.SchemaList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, s := range list.Items {
			if s.Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: s.Name, Namespace: s.Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToPermissions returns reconcile requests for all Permission CRs that reference the given Secret.
func mapSecretToPermissions(ctx context.Context, c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.PermissionList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, p := range list.Items {
			if p.Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
				})
			}
		}
		return requests
	}
}

// requeueWithJitter returns a RequeueAfter duration with ±20% jitter to avoid thundering herd.
func requeueWithJitter(base time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(base*2/5))) - base/5
	return base + jitter
}
