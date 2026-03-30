package controller

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
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

// --- Managed mode tests ---

func testManagedSQLServer(name string) *v1alpha1.SQLServer {
	port := int32(1433)
	replicas := int32(1)
	image := "mcr.microsoft.com/mssql/server:2022-latest"
	edition := "Developer"
	storageSize := "10Gi"
	svcType := corev1.ServiceTypeClusterIP
	return &v1alpha1.SQLServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: v1alpha1.SQLServerSpec{
			Port:       &port,
			AuthMethod: v1alpha1.AuthSqlLogin,
			CredentialsSecret: &v1alpha1.CrossNamespaceSecretReference{
				Name: "sa-credentials",
			},
			Instance: &v1alpha1.InstanceSpec{
				Image:            &image,
				SAPasswordSecret: v1alpha1.SecretReference{Name: "mssql-sa-password"},
				AcceptEULA:       true,
				Edition:          &edition,
				Replicas:         &replicas,
				StorageSize:      &storageSize,
				ServiceType:      &svcType,
			},
		},
	}
}

func saPasswordSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mssql-sa-password",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"MSSQL_SA_PASSWORD": []byte("P@ssw0rd!"),
		},
	}
}

func TestSQLServer_Managed_CreatesStatefulSetAndServices(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("managed-sql")
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "managed-sql", Namespace: "default"}}

	// First reconcile: adds finalizer
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if !result.Requeue {
		t.Error("expected requeue after adding finalizer")
	}

	// Second reconcile: creates resources, but STS won't be ready
	result, err = r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	// Verify StatefulSet was created
	var sts appsv1.StatefulSet
	if err := r.Get(context.Background(), types.NamespacedName{Name: "managed-sql", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	if *sts.Spec.Replicas != 1 {
		t.Errorf("expected 1 replica, got %d", *sts.Spec.Replicas)
	}
	if sts.Spec.Template.Spec.Containers[0].Image != "mcr.microsoft.com/mssql/server:2022-latest" {
		t.Errorf("unexpected image: %s", sts.Spec.Template.Spec.Containers[0].Image)
	}

	// Verify headless Service
	var headlessSvc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Name: "managed-sql-headless", Namespace: "default"}, &headlessSvc); err != nil {
		t.Fatalf("headless Service not found: %v", err)
	}
	if headlessSvc.Spec.ClusterIP != "None" {
		t.Error("expected ClusterIP=None for headless Service")
	}

	// Verify client Service
	var clientSvc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Name: "managed-sql", Namespace: "default"}, &clientSvc); err != nil {
		t.Fatalf("client Service not found: %v", err)
	}

	// Status should show DeploymentProvisioning (STS not ready in fake client)
	var updated v1alpha1.SQLServer
	_ = r.Get(context.Background(), req.NamespacedName, &updated)
	if len(updated.Status.Conditions) == 0 {
		t.Fatal("expected conditions")
	}
	if updated.Status.Conditions[0].Reason != v1alpha1.ReasonDeploymentProvisioning {
		t.Errorf("expected DeploymentProvisioning reason, got %s", updated.Status.Conditions[0].Reason)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue while STS not ready")
	}
}

func TestSQLServer_Managed_ReadyAfterSTSReady(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("managed-sql")
	// Pre-create a ready StatefulSet
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-sql",
			Namespace: "default",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 1,
		},
	}
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret(), sts}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "managed-sql", Namespace: "default"}}

	// First reconcile: adds finalizer
	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Second reconcile: STS is ready, should probe SQL and set Ready=True
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected periodic requeue for health polling")
	}

	var updated v1alpha1.SQLServer
	_ = r.Get(context.Background(), req.NamespacedName, &updated)
	if updated.Status.Host != "managed-sql.default.svc.cluster.local" {
		t.Errorf("unexpected status.host: %s", updated.Status.Host)
	}
	if updated.Status.ServerVersion == "" {
		t.Error("expected ServerVersion to be set")
	}
	if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Error("expected Ready=True")
	}
}

func TestSQLServer_Managed_Idempotent(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("managed-sql")
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-sql", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret(), sts}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "managed-sql", Namespace: "default"}}

	// Reconcile 3 times
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i+1, err)
		}
	}

	var updated v1alpha1.SQLServer
	_ = r.Get(context.Background(), req.NamespacedName, &updated)
	if len(updated.Status.Conditions) != 1 {
		t.Errorf("expected exactly 1 condition, got %d", len(updated.Status.Conditions))
	}
}

func TestSQLServer_Managed_StatusHostPopulated(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("mydb")
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "mydb", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret(), sts}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "mydb", Namespace: "default"}}
	// Finalizer
	_, _ = r.Reconcile(context.Background(), req)
	// Actual reconcile
	_, _ = r.Reconcile(context.Background(), req)

	var updated v1alpha1.SQLServer
	_ = r.Get(context.Background(), req.NamespacedName, &updated)
	expected := "mydb.default.svc.cluster.local"
	if updated.Status.Host != expected {
		t.Errorf("expected status.host=%s, got %s", expected, updated.Status.Host)
	}
}

func TestSQLServer_Managed_ConnectionFailed(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	mockSQL.ConnectError = errors.New("connection refused")
	srv := testManagedSQLServer("managed-sql")
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-sql", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret(), sts}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "managed-sql", Namespace: "default"}}
	// Finalizer
	_, _ = r.Reconcile(context.Background(), req)
	// Actual reconcile
	_, err := r.Reconcile(context.Background(), req)
	if err == nil {
		t.Fatal("expected error on connection failure")
	}
}

func TestSQLServer_Managed_SchedulingConstraints(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("managed-sql")
	srv.Spec.Instance.NodeSelector = map[string]string{"disktype": "ssd"}
	srv.Spec.Instance.Tolerations = []corev1.Toleration{
		{Key: "dedicated", Value: "mssql", Effect: corev1.TaintEffectNoSchedule},
	}

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "managed-sql", Namespace: "default"}}
	// Finalizer + create resources
	_, _ = r.Reconcile(context.Background(), req)
	_, _ = r.Reconcile(context.Background(), req)

	var sts appsv1.StatefulSet
	if err := r.Get(context.Background(), types.NamespacedName{Name: "managed-sql", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	if sts.Spec.Template.Spec.NodeSelector["disktype"] != "ssd" {
		t.Error("expected nodeSelector to be set on StatefulSet")
	}
	if len(sts.Spec.Template.Spec.Tolerations) != 1 {
		t.Error("expected tolerations to be set on StatefulSet")
	}
}

func TestSQLServer_Managed_ClusterMode_HADREnabled(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("cluster-sql")
	replicas := int32(3)
	srv.Spec.Instance.Replicas = &replicas

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-sql", Namespace: "default"}}
	// Finalizer
	_, _ = r.Reconcile(context.Background(), req)
	// Create resources
	_, _ = r.Reconcile(context.Background(), req)

	var sts appsv1.StatefulSet
	if err := r.Get(context.Background(), types.NamespacedName{Name: "cluster-sql", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("StatefulSet not found: %v", err)
	}
	if *sts.Spec.Replicas != 3 {
		t.Errorf("expected 3 replicas, got %d", *sts.Spec.Replicas)
	}

	// Check HADR is enabled in env
	container := sts.Spec.Template.Spec.Containers[0]
	foundHADR := false
	for _, env := range container.Env {
		if env.Name == "MSSQL_ENABLE_HADR" && env.Value == "1" {
			foundHADR = true
		}
	}
	if !foundHADR {
		t.Error("expected MSSQL_ENABLE_HADR=1 for cluster mode")
	}

	// Check HADR port
	foundHADRPort := false
	for _, port := range container.Ports {
		if port.Name == "hadr" && port.ContainerPort == 5022 {
			foundHADRPort = true
		}
	}
	if !foundHADRPort {
		t.Error("expected HADR port 5022 for cluster mode")
	}

	// Check headless service has HADR port
	var headlessSvc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Name: "cluster-sql-headless", Namespace: "default"}, &headlessSvc); err != nil {
		t.Fatalf("headless Service not found: %v", err)
	}
	foundHADRSvcPort := false
	for _, port := range headlessSvc.Spec.Ports {
		if port.Name == "hadr" && port.Port == 5022 {
			foundHADRSvcPort = true
		}
	}
	if !foundHADRSvcPort {
		t.Error("expected HADR port on headless Service for cluster mode")
	}
}

func TestSQLServer_Managed_NoCredentialsSecret_FallbackToSAPassword(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("managed-sql")
	// Remove credentialsSecret — should fall back to sa + saPasswordSecret
	srv.Spec.CredentialsSecret = nil

	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-sql", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	// Only saPasswordSecret, no sa-credentials secret
	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saPasswordSecret(), sts}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "managed-sql", Namespace: "default"}}
	// Finalizer
	_, _ = r.Reconcile(context.Background(), req)
	// Actual reconcile — should succeed using saPasswordSecret
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected periodic requeue for health polling")
	}

	var updated v1alpha1.SQLServer
	_ = r.Get(context.Background(), req.NamespacedName, &updated)
	if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Error("expected Ready=True when falling back to saPasswordSecret")
	}
}

func TestResolveServerCredentials_ManagedFallback(t *testing.T) {
	srv := &v1alpha1.SQLServer{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-sql", Namespace: "default"},
		Spec: v1alpha1.SQLServerSpec{
			// No CredentialsSecret — managed mode with saPasswordSecret
			Instance: &v1alpha1.InstanceSpec{
				SAPasswordSecret: v1alpha1.SecretReference{Name: "mssql-sa-password"},
				AcceptEULA:       true,
			},
		},
		Status: v1alpha1.SQLServerStatus{
			Host: "managed-sql.default.svc.cluster.local",
		},
	}
	saSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mssql-sa-password", Namespace: "default"},
		Data:       map[string][]byte{"MSSQL_SA_PASSWORD": []byte("P@ssw0rd!")},
	}
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(srv, saSecret).Build()

	srvName := "managed-sql"
	ref := v1alpha1.ServerReference{SQLServerRef: &srvName}
	username, password, err := resolveServerCredentials(context.Background(), k8sClient, "default", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if username != "sa" {
		t.Errorf("expected username=sa, got %s", username)
	}
	if password != "P@ssw0rd!" {
		t.Errorf("expected password=P@ssw0rd!, got %s", password)
	}
}

func TestResolveServerCredentials_WithCredentialsSecret(t *testing.T) {
	srv := &v1alpha1.SQLServer{
		ObjectMeta: metav1.ObjectMeta{Name: "ext-sql", Namespace: "default"},
		Spec: v1alpha1.SQLServerSpec{
			Host:              "external.svc",
			CredentialsSecret: &v1alpha1.CrossNamespaceSecretReference{Name: "custom-creds"},
		},
	}
	credsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("admin"), "password": []byte("secret")},
	}
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(srv, credsSecret).Build()

	srvName := "ext-sql"
	ref := v1alpha1.ServerReference{SQLServerRef: &srvName}
	username, password, err := resolveServerCredentials(context.Background(), k8sClient, "default", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if username != "admin" {
		t.Errorf("expected username=admin, got %s", username)
	}
	if password != "secret" {
		t.Errorf("expected password=secret, got %s", password)
	}
}

func TestResolveServerReference_ManagedMode_UsesStatusHost(t *testing.T) {
	srv := &v1alpha1.SQLServer{
		ObjectMeta: metav1.ObjectMeta{Name: "managed-sql", Namespace: "default"},
		Spec: v1alpha1.SQLServerSpec{
			// No Host — managed mode
			CredentialsSecret: &v1alpha1.CrossNamespaceSecretReference{Name: "sa-creds"},
		},
		Status: v1alpha1.SQLServerStatus{
			Host: "managed-sql.default.svc.cluster.local",
		},
	}
	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(srv).Build()

	srvName := "managed-sql"
	ref := v1alpha1.ServerReference{SQLServerRef: &srvName}
	resolved, err := resolveServerReference(context.Background(), k8sClient, "default", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Host != "managed-sql.default.svc.cluster.local" {
		t.Errorf("expected managed host, got %s", resolved.Host)
	}
}
