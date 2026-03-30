package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

const (
	hadrEndpointPort = 5022
	sqlPort          = 1433
)

// reconcileStatefulSet ensures the StatefulSet exists and is up to date.
func (r *SQLServerReconciler) reconcileStatefulSet(ctx context.Context, srv *v1alpha1.SQLServer) error {
	inst := srv.Spec.Instance
	desired := r.desiredStatefulSet(srv)

	if err := controllerutil.SetControllerReference(srv, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on StatefulSet: %w", err)
	}

	var existing appsv1.StatefulSet
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update mutable fields: image, env, resources, replicas, scheduling
	needsUpdate := false

	if !equality.Semantic.DeepEqual(existing.Spec.Replicas, desired.Spec.Replicas) {
		existing.Spec.Replicas = desired.Spec.Replicas
		needsUpdate = true
	}

	if len(existing.Spec.Template.Spec.Containers) > 0 && len(desired.Spec.Template.Spec.Containers) > 0 {
		existingContainer := &existing.Spec.Template.Spec.Containers[0]
		desiredContainer := &desired.Spec.Template.Spec.Containers[0]

		if existingContainer.Image != desiredContainer.Image {
			existingContainer.Image = desiredContainer.Image
			needsUpdate = true
		}
		if !equality.Semantic.DeepEqual(existingContainer.Env, desiredContainer.Env) {
			existingContainer.Env = desiredContainer.Env
			needsUpdate = true
		}
		if inst.Resources != nil && !equality.Semantic.DeepEqual(existingContainer.Resources, *inst.Resources) {
			existingContainer.Resources = *inst.Resources
			needsUpdate = true
		}
	} else if len(existing.Spec.Template.Spec.Containers) == 0 {
		existing.Spec.Template = desired.Spec.Template
		needsUpdate = true
	}

	// Scheduling
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.NodeSelector, desired.Spec.Template.Spec.NodeSelector) {
		existing.Spec.Template.Spec.NodeSelector = desired.Spec.Template.Spec.NodeSelector
		needsUpdate = true
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.Tolerations, desired.Spec.Template.Spec.Tolerations) {
		existing.Spec.Template.Spec.Tolerations = desired.Spec.Template.Spec.Tolerations
		needsUpdate = true
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.Affinity, desired.Spec.Template.Spec.Affinity) {
		existing.Spec.Template.Spec.Affinity = desired.Spec.Template.Spec.Affinity
		needsUpdate = true
	}
	if !equality.Semantic.DeepEqual(existing.Spec.Template.Spec.TopologySpreadConstraints, desired.Spec.Template.Spec.TopologySpreadConstraints) {
		existing.Spec.Template.Spec.TopologySpreadConstraints = desired.Spec.Template.Spec.TopologySpreadConstraints
		needsUpdate = true
	}

	if needsUpdate {
		return r.Update(ctx, &existing)
	}
	return nil
}

// reconcileHeadlessService ensures the headless Service exists for inter-pod DNS.
func (r *SQLServerReconciler) reconcileHeadlessService(ctx context.Context, srv *v1alpha1.SQLServer) error {
	desired := r.desiredHeadlessService(srv)
	if err := controllerutil.SetControllerReference(srv, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on headless Service: %w", err)
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	return err
}

// reconcileClientService ensures the client-facing Service exists.
func (r *SQLServerReconciler) reconcileClientService(ctx context.Context, srv *v1alpha1.SQLServer) error {
	desired := r.desiredClientService(srv)
	if err := controllerutil.SetControllerReference(srv, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on client Service: %w", err)
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update service type if changed
	if existing.Spec.Type != desired.Spec.Type {
		existing.Spec.Type = desired.Spec.Type
		return r.Update(ctx, &existing)
	}
	return nil
}

// isStatefulSetReady checks if all replicas are ready.
func (r *SQLServerReconciler) isStatefulSetReady(ctx context.Context, srv *v1alpha1.SQLServer) (ready bool, readyReplicas int32, err error) {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, types.NamespacedName{Name: srv.Name, Namespace: srv.Namespace}, &sts); err != nil {
		return false, 0, err
	}
	desired := int32(1)
	if srv.Spec.Instance.Replicas != nil {
		desired = *srv.Spec.Instance.Replicas
	}
	return sts.Status.ReadyReplicas >= desired, sts.Status.ReadyReplicas, nil
}

func (r *SQLServerReconciler) desiredStatefulSet(srv *v1alpha1.SQLServer) *appsv1.StatefulSet {
	inst := srv.Spec.Instance
	labels := instanceLabels(srv)

	replicas := int32(1)
	if inst.Replicas != nil {
		replicas = *inst.Replicas
	}

	image := "mcr.microsoft.com/mssql/server:2022-latest"
	if inst.Image != nil {
		image = *inst.Image
	}

	envVars := []corev1.EnvVar{
		{Name: "ACCEPT_EULA", Value: "Y"},
		{
			Name: "MSSQL_SA_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: inst.SAPasswordSecret.Name},
					Key:                  "MSSQL_SA_PASSWORD",
				},
			},
		},
	}

	if inst.Edition != nil && *inst.Edition != "Developer" {
		envVars = append(envVars, corev1.EnvVar{Name: "MSSQL_PID", Value: *inst.Edition})
	}

	if replicas > 1 {
		envVars = append(envVars, corev1.EnvVar{Name: "MSSQL_ENABLE_HADR", Value: "1"})
	}

	container := corev1.Container{
		Name:  "mssql",
		Image: image,
		Env:   envVars,
		Ports: []corev1.ContainerPort{
			{Name: "sql", ContainerPort: sqlPort},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "mssql-data", MountPath: "/var/opt/mssql"},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(sqlPort)},
			},
			InitialDelaySeconds: 20,
			PeriodSeconds:       10,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(sqlPort)},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       15,
		},
	}

	if replicas > 1 {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Name: "hadr", ContainerPort: hadrEndpointPort,
		})
	}

	if inst.Resources != nil {
		container.Resources = *inst.Resources
	}

	storageSize := "10Gi"
	if inst.StorageSize != nil {
		storageSize = *inst.StorageSize
	}

	vctSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse(storageSize),
			},
		},
	}
	if inst.StorageClassName != nil {
		vctSpec.StorageClassName = inst.StorageClassName
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      srv.Name,
			Namespace: srv.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: srv.Name + "-headless",
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers:                []corev1.Container{container},
					NodeSelector:              inst.NodeSelector,
					Tolerations:               inst.Tolerations,
					Affinity:                  inst.Affinity,
					TopologySpreadConstraints: inst.TopologySpreadConstraints,
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "mssql-data"},
					Spec:       vctSpec,
				},
			},
		},
	}

	return sts
}

func (r *SQLServerReconciler) desiredHeadlessService(srv *v1alpha1.SQLServer) *corev1.Service {
	labels := instanceLabels(srv)
	ports := []corev1.ServicePort{
		{Name: "sql", Port: sqlPort, TargetPort: intstr.FromInt32(sqlPort)},
	}
	replicas := int32(1)
	if srv.Spec.Instance.Replicas != nil {
		replicas = *srv.Spec.Instance.Replicas
	}
	if replicas > 1 {
		ports = append(ports, corev1.ServicePort{
			Name: "hadr", Port: hadrEndpointPort, TargetPort: intstr.FromInt32(hadrEndpointPort),
		})
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      srv.Name + "-headless",
			Namespace: srv.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  labels,
			Ports:     ports,
		},
	}
}

func (r *SQLServerReconciler) desiredClientService(srv *v1alpha1.SQLServer) *corev1.Service {
	labels := instanceLabels(srv)
	svcType := corev1.ServiceTypeClusterIP
	if srv.Spec.Instance.ServiceType != nil {
		svcType = *srv.Spec.Instance.ServiceType
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      srv.Name,
			Namespace: srv.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "sql", Port: sqlPort, TargetPort: intstr.FromInt32(sqlPort)},
			},
		},
	}
}

// managedHost returns the FQDN for the client service.
func managedHost(srv *v1alpha1.SQLServer) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", srv.Name, srv.Namespace)
}

// replicaHost returns the FQDN of a specific replica pod.
func replicaHost(srv *v1alpha1.SQLServer, index int) string {
	return fmt.Sprintf("%s-%d.%s-headless.%s.svc.cluster.local", srv.Name, index, srv.Name, srv.Namespace)
}

func instanceLabels(srv *v1alpha1.SQLServer) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "mssql",
		"app.kubernetes.io/instance":   srv.Name,
		"app.kubernetes.io/managed-by": "mssql-operator",
	}
}
