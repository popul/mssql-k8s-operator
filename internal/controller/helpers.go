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
func getCredentialsFromSecret(ctx context.Context, c client.Client, namespace, secretName string) (user, pass string, err error) {
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
func mapSecretToDatabases(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.DatabaseList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToLogins returns reconcile requests for all Login CRs that reference the given Secret.
func mapSecretToLogins(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.LoginList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.Server.CredentialsSecret.Name == obj.GetName() ||
				list.Items[i].Spec.PasswordSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToDatabaseUsers returns reconcile requests for all DatabaseUser CRs that reference the given Secret.
func mapSecretToDatabaseUsers(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.DatabaseUserList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToSchemas returns reconcile requests for all Schema CRs that reference the given Secret.
func mapSecretToSchemas(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.SchemaList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToPermissions returns reconcile requests for all Permission CRs that reference the given Secret.
func mapSecretToPermissions(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.PermissionList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToBackups returns reconcile requests for all Backup CRs that reference the given Secret.
func mapSecretToBackups(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.BackupList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToRestores returns reconcile requests for all Restore CRs that reference the given Secret.
func mapSecretToRestores(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.RestoreList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			if list.Items[i].Spec.Server.CredentialsSecret.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
				})
			}
		}
		return requests
	}
}

// mapSecretToAGs returns reconcile requests for all AvailabilityGroup CRs that reference the given Secret.
func mapSecretToAGs(c client.Client) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.AvailabilityGroupList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range list.Items {
			for j := range list.Items[i].Spec.Replicas {
				if list.Items[i].Spec.Replicas[j].Server.CredentialsSecret.Name == obj.GetName() {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{Name: list.Items[i].Name, Namespace: list.Items[i].Namespace},
					})
					break
				}
			}
		}
		return requests
	}
}

// resolveServerReference resolves a ServerReference. If sqlServerRef is set, it fetches
// the SQLServer CR and returns the equivalent inline ServerReference. Otherwise returns as-is.
func resolveServerReference(ctx context.Context, c client.Client, namespace string, ref v1alpha1.ServerReference) (v1alpha1.ServerReference, error) {
	if ref.SQLServerRef == nil {
		return ref, nil
	}
	var srv v1alpha1.SQLServer
	if err := c.Get(ctx, types.NamespacedName{Name: *ref.SQLServerRef, Namespace: namespace}, &srv); err != nil {
		return ref, fmt.Errorf("failed to resolve SQLServer %q: %w", *ref.SQLServerRef, err)
	}
	resolved := v1alpha1.ServerReference{
		Host: srv.Spec.Host,
		Port: srv.Spec.Port,
		TLS:  srv.Spec.TLS,
	}
	if srv.Spec.CredentialsSecret != nil {
		resolved.CredentialsSecret = v1alpha1.SecretReference{
			Name: srv.Spec.CredentialsSecret.Name,
		}
	}
	return resolved, nil
}

// requeueWithJitter returns a RequeueAfter duration with ±20% jitter to avoid thundering herd.
func requeueWithJitter(base time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(base*2/5))) - base/5
	return base + jitter
}
