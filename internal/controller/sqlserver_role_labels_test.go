package controller

import (
	"context"
	"fmt"
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

// =============================================================================
// Role label constants
// =============================================================================

func TestRoleLabelConstants(t *testing.T) {
	if LabelRole != "mssql.popul.io/role" {
		t.Errorf("expected label key mssql.popul.io/role, got %s", LabelRole)
	}
	if RolePrimary != "primary" {
		t.Errorf("expected role value primary, got %s", RolePrimary)
	}
	if RoleSecondary != "secondary" {
		t.Errorf("expected role value secondary, got %s", RoleSecondary)
	}
}

// =============================================================================
// Client service selector includes role=primary for multi-replica
// =============================================================================

func TestDesiredClientService_SingleReplica_NoRoleSelector(t *testing.T) {
	srv := testManagedSQLServer("single")

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, sqlclient.NewMockClient())
	svc := r.desiredClientService(srv)

	if _, ok := svc.Spec.Selector[LabelRole]; ok {
		t.Error("single-replica client service should NOT have a role selector")
	}
}

func TestDesiredClientService_MultiReplica_HasPrimaryRoleSelector(t *testing.T) {
	srv := testManagedSQLServer("cluster")
	replicas := int32(3)
	srv.Spec.Instance.Replicas = &replicas

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, sqlclient.NewMockClient())
	svc := r.desiredClientService(srv)

	if svc.Spec.Selector[LabelRole] != RolePrimary {
		t.Errorf("multi-replica client service should select role=primary, got %q", svc.Spec.Selector[LabelRole])
	}
}

// =============================================================================
// Read-only service
// =============================================================================

func TestDesiredReadOnlyService_MultiReplica(t *testing.T) {
	srv := testManagedSQLServer("cluster")
	replicas := int32(3)
	srv.Spec.Instance.Replicas = &replicas

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, sqlclient.NewMockClient())
	svc := r.desiredReadOnlyService(srv)

	if svc.Name != "cluster-readonly" {
		t.Errorf("expected service name cluster-readonly, got %s", svc.Name)
	}
	if svc.Spec.Selector[LabelRole] != RoleSecondary {
		t.Errorf("read-only service should select role=secondary, got %q", svc.Spec.Selector[LabelRole])
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 1433 {
		t.Error("expected SQL port 1433 on read-only service")
	}
}

func TestDesiredReadOnlyService_RespectsServiceType(t *testing.T) {
	srv := testManagedSQLServer("cluster")
	replicas := int32(3)
	srv.Spec.Instance.Replicas = &replicas
	svcType := corev1.ServiceTypeLoadBalancer
	srv.Spec.Instance.ServiceType = &svcType

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, sqlclient.NewMockClient())
	svc := r.desiredReadOnlyService(srv)

	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Errorf("expected LoadBalancer, got %s", svc.Spec.Type)
	}
}

// =============================================================================
// Read-only service is created for multi-replica, not for single-replica
// =============================================================================

func TestManagedReconcile_MultiReplica_CreatesReadOnlyService(t *testing.T) {
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

	var roSvc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Name: "cluster-sql-readonly", Namespace: "default"}, &roSvc); err != nil {
		t.Fatalf("read-only Service not found: %v", err)
	}
	if roSvc.Spec.Selector[LabelRole] != RoleSecondary {
		t.Errorf("expected role=secondary selector, got %q", roSvc.Spec.Selector[LabelRole])
	}
}

func TestManagedReconcile_SingleReplica_NoReadOnlyService(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("single-sql")

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "single-sql", Namespace: "default"}}
	_, _ = r.Reconcile(context.Background(), req)
	_, _ = r.Reconcile(context.Background(), req)

	var roSvc corev1.Service
	err := r.Get(context.Background(), types.NamespacedName{Name: "single-sql-readonly", Namespace: "default"}, &roSvc)
	if err == nil {
		t.Error("single-replica should NOT create a read-only Service")
	}
}

// =============================================================================
// reconcileReplicaRoleLabels — labels pods with primary/secondary role
// =============================================================================

func TestReconcileReplicaRoleLabels_LabelsPods(t *testing.T) {
	srv := testManagedSQLServer("mssql")
	replicas := int32(3)
	srv.Spec.Instance.Replicas = &replicas

	// Create pods that belong to the StatefulSet
	pods := make([]runtime.Object, 3)
	for i := int32(0); i < 3; i++ {
		pods[i] = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("mssql-%d", i),
				Namespace: "default",
				Labels:    instanceLabels(srv),
			},
		}
	}

	objs := append([]runtime.Object{srv, saSecret(), saPasswordSecret()}, pods...)
	r, _ := newTestSQLServerReconciler(objs, sqlclient.NewMockClient())

	// Set primary to pod-1 (the FQDN form)
	primaryReplica := "mssql-1.mssql-headless.default.svc.cluster.local"
	if err := r.reconcileReplicaRoleLabels(context.Background(), srv, primaryReplica); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify pod labels
	for i := int32(0); i < 3; i++ {
		var pod corev1.Pod
		if err := r.Get(context.Background(), types.NamespacedName{Name: fmt.Sprintf("mssql-%d", i), Namespace: "default"}, &pod); err != nil {
			t.Fatalf("pod mssql-%d not found: %v", i, err)
		}
		expectedRole := RoleSecondary
		if i == 1 {
			expectedRole = RolePrimary
		}
		if pod.Labels[LabelRole] != expectedRole {
			t.Errorf("pod mssql-%d: expected role=%s, got %s", i, expectedRole, pod.Labels[LabelRole])
		}
	}
}

func TestReconcileReplicaRoleLabels_PodNameFormat(t *testing.T) {
	srv := testManagedSQLServer("mssql")
	replicas := int32(2)
	srv.Spec.Instance.Replicas = &replicas

	pods := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mssql-0",
				Namespace: "default",
				Labels:    instanceLabels(srv),
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mssql-1",
				Namespace: "default",
				Labels:    instanceLabels(srv),
			},
		},
	}

	objs := append([]runtime.Object{srv, saSecret(), saPasswordSecret()}, pods...)
	r, _ := newTestSQLServerReconciler(objs, sqlclient.NewMockClient())

	// Primary given as short pod name (without FQDN)
	if err := r.reconcileReplicaRoleLabels(context.Background(), srv, "mssql-0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pod0, pod1 corev1.Pod
	_ = r.Get(context.Background(), types.NamespacedName{Name: "mssql-0", Namespace: "default"}, &pod0)
	_ = r.Get(context.Background(), types.NamespacedName{Name: "mssql-1", Namespace: "default"}, &pod1)

	if pod0.Labels[LabelRole] != RolePrimary {
		t.Errorf("expected mssql-0 to be primary, got %s", pod0.Labels[LabelRole])
	}
	if pod1.Labels[LabelRole] != RoleSecondary {
		t.Errorf("expected mssql-1 to be secondary, got %s", pod1.Labels[LabelRole])
	}
}

func TestReconcileReplicaRoleLabels_Idempotent(t *testing.T) {
	srv := testManagedSQLServer("mssql")
	replicas := int32(2)
	srv.Spec.Instance.Replicas = &replicas

	pods := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mssql-0",
				Namespace: "default",
				Labels: func() map[string]string {
					l := instanceLabels(srv)
					l[LabelRole] = RolePrimary
					return l
				}(),
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mssql-1",
				Namespace: "default",
				Labels: func() map[string]string {
					l := instanceLabels(srv)
					l[LabelRole] = RoleSecondary
					return l
				}(),
			},
		},
	}

	objs := append([]runtime.Object{srv, saSecret(), saPasswordSecret()}, pods...)
	r, _ := newTestSQLServerReconciler(objs, sqlclient.NewMockClient())

	// Call twice — no error, same result
	if err := r.reconcileReplicaRoleLabels(context.Background(), srv, "mssql-0"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := r.reconcileReplicaRoleLabels(context.Background(), srv, "mssql-0"); err != nil {
		t.Fatalf("second call: %v", err)
	}

	var pod0 corev1.Pod
	_ = r.Get(context.Background(), types.NamespacedName{Name: "mssql-0", Namespace: "default"}, &pod0)
	if pod0.Labels[LabelRole] != RolePrimary {
		t.Errorf("expected mssql-0 to still be primary")
	}
}

// =============================================================================
// Client service selector is updated for multi-replica
// =============================================================================

func TestManagedReconcile_MultiReplica_ClientServiceHasPrimarySelector(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("cluster-sql")
	replicas := int32(3)
	srv.Spec.Instance.Replicas = &replicas

	r, _ := newTestSQLServerReconciler([]runtime.Object{srv, saSecret(), saPasswordSecret()}, mockSQL)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-sql", Namespace: "default"}}
	_, _ = r.Reconcile(context.Background(), req)
	_, _ = r.Reconcile(context.Background(), req)

	var clientSvc corev1.Service
	if err := r.Get(context.Background(), types.NamespacedName{Name: "cluster-sql", Namespace: "default"}, &clientSvc); err != nil {
		t.Fatalf("client Service not found: %v", err)
	}
	if clientSvc.Spec.Selector[LabelRole] != RolePrimary {
		t.Errorf("expected role=primary selector on client service, got %q", clientSvc.Spec.Selector[LabelRole])
	}
}

// =============================================================================
// Full reconcile with ready STS + AG — labels are applied
// =============================================================================

func TestManagedReconcile_MultiReplica_LabelsAppliedAfterAG(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	srv := testManagedSQLServer("mssql")
	replicas := int32(2)
	srv.Spec.Instance.Replicas = &replicas
	agName := "mssql-ag"
	srv.Spec.Instance.AvailabilityGroup = &v1alpha1.ManagedAGSpec{
		AGName: &agName,
	}

	// Pre-create ready StatefulSet
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "mssql", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 2},
	}

	// Pre-create pods
	pod0 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mssql-0",
			Namespace: "default",
			Labels:    instanceLabels(srv),
		},
	}
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mssql-1",
			Namespace: "default",
			Labels:    instanceLabels(srv),
		},
	}

	// Pre-create AG CR with primary status set
	agCR := &v1alpha1.AvailabilityGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "mssql-ag", Namespace: "default"},
		Spec: v1alpha1.AvailabilityGroupSpec{
			AGName: "mssql-ag",
			Replicas: []v1alpha1.AGReplicaSpec{
				{ServerName: "mssql-0", EndpointURL: "TCP://mssql-0:5022",
					Server: v1alpha1.ServerReference{Host: "mssql-0", CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"}}},
				{ServerName: "mssql-1", EndpointURL: "TCP://mssql-1:5022",
					Server: v1alpha1.ServerReference{Host: "mssql-1", CredentialsSecret: v1alpha1.SecretReference{Name: "sa-credentials"}}},
			},
		},
		Status: v1alpha1.AvailabilityGroupStatus{
			PrimaryReplica: "mssql-0",
		},
	}

	scheme := newScheme()
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SQLServer{}, &v1alpha1.AvailabilityGroup{}).
		WithRuntimeObjects(srv, saSecret(), saPasswordSecret(), sts, agCR, pod0, pod1).
		Build()
	recorder := record.NewFakeRecorder(20)

	// Manually set AG status (fake client with status subresource requires explicit status write)
	agCR.Status.PrimaryReplica = "mssql-0"
	_ = k8sClient.Status().Update(context.Background(), agCR)

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

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "mssql", Namespace: "default"}}
	// Finalizer
	_, _ = r.Reconcile(context.Background(), req)
	// Actual reconcile
	_, _ = r.Reconcile(context.Background(), req)

	// Verify pod labels
	var gotPod0, gotPod1 corev1.Pod
	_ = r.Get(context.Background(), types.NamespacedName{Name: "mssql-0", Namespace: "default"}, &gotPod0)
	_ = r.Get(context.Background(), types.NamespacedName{Name: "mssql-1", Namespace: "default"}, &gotPod1)

	if gotPod0.Labels[LabelRole] != RolePrimary {
		t.Errorf("expected mssql-0 to be primary, got %q", gotPod0.Labels[LabelRole])
	}
	if gotPod1.Labels[LabelRole] != RoleSecondary {
		t.Errorf("expected mssql-1 to be secondary, got %q", gotPod1.Labels[LabelRole])
	}
}
