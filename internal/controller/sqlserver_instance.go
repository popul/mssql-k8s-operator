package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

const (
	hadrEndpointPort = 5022
	sqlPort          = 1433

	// LabelRole is the label key used to identify the role of a SQL Server pod in HA mode.
	LabelRole = "mssql.popul.io/role"
	// RolePrimary is the label value for the primary replica.
	RolePrimary = "primary"
	// RoleSecondary is the label value for secondary replicas.
	RoleSecondary = "secondary"
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

	// Update service type or selector if changed
	needsUpdate := false
	if existing.Spec.Type != desired.Spec.Type {
		existing.Spec.Type = desired.Spec.Type
		needsUpdate = true
	}
	if existing.Spec.Selector[LabelRole] != desired.Spec.Selector[LabelRole] {
		existing.Spec.Selector = desired.Spec.Selector
		needsUpdate = true
	}
	if needsUpdate {
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

// reconcileConfigMap ensures the mssql.conf ConfigMap exists and is up to date.
func (r *SQLServerReconciler) reconcileConfigMap(ctx context.Context, srv *v1alpha1.SQLServer) error {
	desired := r.desiredConfigMap(srv)
	if err := controllerutil.SetControllerReference(srv, desired, r.Scheme); err != nil {
		return err
	}

	var existing corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if existing.Data["mssql.conf"] != desired.Data["mssql.conf"] {
		existing.Data = desired.Data
		return r.Update(ctx, &existing)
	}
	return nil
}

// desiredConfigMap builds the mssql.conf ConfigMap from the instance spec.
func (r *SQLServerReconciler) desiredConfigMap(srv *v1alpha1.SQLServer) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      srv.Name + "-config",
			Namespace: srv.Namespace,
			Labels:    instanceLabels(srv),
		},
		Data: map[string]string{
			"mssql.conf": buildMSSQLConf(srv),
		},
	}
}

// buildMSSQLConf generates the mssql.conf content.
// It uses the raw user config and auto-appends memorylimitmb if not present.
func buildMSSQLConf(srv *v1alpha1.SQLServer) string {
	inst := srv.Spec.Instance

	conf := ""
	if inst.Config != nil {
		conf = *inst.Config
	}

	// Auto-append memorylimitmb if not set and memory limit is defined
	if !strings.Contains(conf, "memorylimitmb") && inst.Resources != nil {
		if memLimit, ok := inst.Resources.Limits[corev1.ResourceMemory]; ok {
			memMB := memLimit.Value() / (1024 * 1024)
			autoLimit := memMB * 80 / 100
			if autoLimit > 0 {
				if !strings.Contains(conf, "[memory]") {
					conf += "\n[memory]\n"
				}
				conf += fmt.Sprintf("memorylimitmb = %d\n", autoLimit)
			}
		}
	}

	return conf
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
			{Name: "mssql-config", MountPath: "/var/opt/mssql/mssql.conf", SubPath: "mssql.conf", ReadOnly: true},
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

	volumes := []corev1.Volume{
		{
			Name: "mssql-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: srv.Name + "-config"},
				},
			},
		},
	}
	if replicas > 1 {
		container.Ports = append(container.Ports, corev1.ContainerPort{
			Name: "hadr", ContainerPort: hadrEndpointPort,
		})

		// Mount all replica cert secrets so each pod can import peer certificates.
		// Each secret is mounted at /var/opt/mssql/certs/{i}/ with tls.crt, tls.key, password.
		for i := int32(0); i < replicas; i++ {
			secretName := fmt.Sprintf("%s-cert-%d", srv.Name, i)
			volumeName := fmt.Sprintf("cert-%d", i)
			mountPath := fmt.Sprintf("/var/opt/mssql/certs/%d", i)

			volumes = append(volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: secretName,
						Optional:   boolPtr(true), // Secret may not exist yet on first reconciliation
					},
				},
			})
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      volumeName,
				MountPath: mountPath,
				ReadOnly:  true,
			})
		}
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
					Volumes:                   volumes,
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
	selector := instanceLabels(srv)

	// In multi-replica mode, route traffic only to the primary.
	replicas := int32(1)
	if srv.Spec.Instance.Replicas != nil {
		replicas = *srv.Spec.Instance.Replicas
	}
	if replicas > 1 {
		selector[LabelRole] = RolePrimary
	}

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
			Selector: selector,
			Ports: []corev1.ServicePort{
				{Name: "sql", Port: sqlPort, TargetPort: intstr.FromInt32(sqlPort)},
			},
		},
	}
}

// desiredReadOnlyService builds a Service that routes to secondary replicas only.
func (r *SQLServerReconciler) desiredReadOnlyService(srv *v1alpha1.SQLServer) *corev1.Service {
	labels := instanceLabels(srv)
	selector := instanceLabels(srv)
	selector[LabelRole] = RoleSecondary

	svcType := corev1.ServiceTypeClusterIP
	if srv.Spec.Instance.ServiceType != nil {
		svcType = *srv.Spec.Instance.ServiceType
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      srv.Name + "-readonly",
			Namespace: srv.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: selector,
			Ports: []corev1.ServicePort{
				{Name: "sql", Port: sqlPort, TargetPort: intstr.FromInt32(sqlPort)},
			},
		},
	}
}

// reconcileReadOnlyService ensures the read-only Service exists for multi-replica mode.
func (r *SQLServerReconciler) reconcileReadOnlyService(ctx context.Context, srv *v1alpha1.SQLServer) error {
	desired := r.desiredReadOnlyService(srv)
	if err := controllerutil.SetControllerReference(srv, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on read-only Service: %w", err)
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update selector or service type if changed
	needsUpdate := false
	if existing.Spec.Type != desired.Spec.Type {
		existing.Spec.Type = desired.Spec.Type
		needsUpdate = true
	}
	if existing.Spec.Selector[LabelRole] != desired.Spec.Selector[LabelRole] {
		existing.Spec.Selector = desired.Spec.Selector
		needsUpdate = true
	}
	if needsUpdate {
		return r.Update(ctx, &existing)
	}
	return nil
}

// reconcileReplicaRoleLabels labels each pod with its role (primary or secondary).
// primaryReplica can be a FQDN or a short pod name.
func (r *SQLServerReconciler) reconcileReplicaRoleLabels(ctx context.Context, srv *v1alpha1.SQLServer, primaryReplica string) error {
	replicas := int32(1)
	if srv.Spec.Instance.Replicas != nil {
		replicas = *srv.Spec.Instance.Replicas
	}

	// Extract the short pod name from FQDN if needed (e.g. "mssql-0.mssql-headless.ns.svc.cluster.local" → "mssql-0")
	primaryPodName := primaryReplica
	if idx := strings.Index(primaryReplica, "."); idx > 0 {
		primaryPodName = primaryReplica[:idx]
	}

	for i := int32(0); i < replicas; i++ {
		podName := fmt.Sprintf("%s-%d", srv.Name, i)
		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: srv.Namespace}, &pod); err != nil {
			continue // Pod may not exist yet
		}

		desiredRole := RoleSecondary
		if podName == primaryPodName {
			desiredRole = RolePrimary
		}

		if pod.Labels[LabelRole] == desiredRole {
			continue // Already correct
		}

		patch := client.MergeFrom(pod.DeepCopy())
		if pod.Labels == nil {
			pod.Labels = make(map[string]string)
		}
		pod.Labels[LabelRole] = desiredRole
		if err := r.Patch(ctx, &pod, patch); err != nil {
			return fmt.Errorf("failed to label pod %s with role %s: %w", podName, desiredRole, err)
		}
	}
	return nil
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
