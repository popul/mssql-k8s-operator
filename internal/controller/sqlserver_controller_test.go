package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

func newTestSQLServerReconciler(objs []runtime.Object, mockSQL *sqlclient.MockClient) (*SQLServerReconciler, *record.FakeRecorder) {
	scheme := newScheme()
	clientBuilder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.SQLServer{})
	for _, obj := range objs {
		clientBuilder = clientBuilder.WithRuntimeObjects(obj)
	}
	k8sClient := clientBuilder.Build()
	recorder := record.NewFakeRecorder(20)

	r := &SQLServerReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: recorder,
		SQLClientFactory: func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
			if mockSQL.ConnectError != nil {
				return nil, mockSQL.ConnectError
			}
			return mockSQL, nil
		},
	}
	return r, recorder
}

func testSQLServer(name string) *v1alpha1.SQLServer {
	port := int32(1433)
	return &v1alpha1.SQLServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.SQLServerSpec{
			Host:       "mssql.svc",
			Port:       &port,
			AuthMethod: v1alpha1.AuthSqlLogin,
			CredentialsSecret: &v1alpha1.CrossNamespaceSecretReference{
				Name: "sa-credentials",
			},
		},
	}
}

func TestSQLServer_Create_SetsReadyAndVersion(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testSQLServer("test-srv")
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret()}, mockSQL)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-srv", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected periodic requeue for health polling")
	}

	var updated v1alpha1.SQLServer
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-srv", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get SQLServer: %v", err)
	}
	if updated.Status.ServerVersion == "" {
		t.Error("expected ServerVersion to be set")
	}
	if updated.Status.Edition == "" {
		t.Error("expected Edition to be set")
	}
	if updated.Status.LastConnectedTime == nil {
		t.Error("expected LastConnectedTime to be set")
	}
	if len(updated.Status.Conditions) == 0 {
		t.Fatal("expected conditions to be set")
	}
	if updated.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True, got %s", updated.Status.Conditions[0].Status)
	}
}

func TestSQLServer_SecretNotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testSQLServer("test-srv")
	// No secret in cluster
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv}, mockSQL)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-srv", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("should not requeue on permanent error")
	}

	var updated v1alpha1.SQLServer
	_ = r.Get(context.Background(), types.NamespacedName{Name: "test-srv", Namespace: "default"}, &updated)
	if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Reason != v1alpha1.ReasonSecretNotFound {
		t.Error("expected SecretNotFound condition")
	}
}

func TestSQLServer_ConnectionFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errors.New("connection refused")
	srv := testSQLServer("test-srv")
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-srv", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error on connection failure")
	}
}

func TestSQLServer_CrossNamespaceSecret(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	otherNS := "infra"
	srv := testSQLServer("test-srv")
	srv.Spec.CredentialsSecret = &v1alpha1.CrossNamespaceSecretReference{
		Name:      "sa-credentials",
		Namespace: &otherNS,
	}
	crossNSSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sa-credentials",
			Namespace: "infra",
		},
		Data: map[string][]byte{
			"username": []byte("sa"),
			"password": []byte("P@ssw0rd"),
		},
	}
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, crossNSSecret}, mockSQL)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-srv", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected periodic requeue")
	}
}

func TestSQLServer_Idempotent(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testSQLServer("test-srv")
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret()}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-srv", Namespace: "default"}}

	// Reconcile twice
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var updated v1alpha1.SQLServer
	_ = r.Get(context.Background(), req.NamespacedName, &updated)
	if len(updated.Status.Conditions) != 1 {
		t.Errorf("expected exactly 1 condition, got %d", len(updated.Status.Conditions))
	}
}

func TestSQLServer_NotFound(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	r, _ := newTestSQLServerReconciler([]runtime.Object{}, mockSQL)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("should not requeue for deleted CR")
	}
}

func TestResolveServerReference_Inline(t *testing.T) {
	ref := v1alpha1.ServerReference{
		Host:              "direct.svc",
		CredentialsSecret: v1alpha1.SecretReference{Name: "creds"},
	}
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	resolved, err := resolveServerReference(context.Background(), k8sClient, "default", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Host != "direct.svc" {
		t.Errorf("expected host=direct.svc, got %s", resolved.Host)
	}
}

func TestResolveServerReference_ViaSQLServerCR(t *testing.T) {
	port := int32(2433)
	srv := &v1alpha1.SQLServer{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-sql", Namespace: "default"},
		Spec: v1alpha1.SQLServerSpec{
			Host:              "shared.svc",
			Port:              &port,
			CredentialsSecret: &v1alpha1.CrossNamespaceSecretReference{Name: "shared-creds"},
		},
	}
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(srv).Build()

	srvName := "shared-sql"
	ref := v1alpha1.ServerReference{
		SQLServerRef: &srvName,
	}
	resolved, err := resolveServerReference(context.Background(), k8sClient, "default", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Host != "shared.svc" {
		t.Errorf("expected host=shared.svc, got %s", resolved.Host)
	}
	if resolved.Port == nil || *resolved.Port != 2433 {
		t.Error("expected port=2433")
	}
	if resolved.CredentialsSecret.Name != "shared-creds" {
		t.Errorf("expected creds=shared-creds, got %s", resolved.CredentialsSecret.Name)
	}
}
