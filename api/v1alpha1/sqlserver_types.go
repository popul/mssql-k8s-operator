package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AuthenticationMethod defines how the operator authenticates to SQL Server.
// +kubebuilder:validation:Enum=SqlLogin;AzureAD;ManagedIdentity
type AuthenticationMethod string

const (
	AuthSqlLogin        AuthenticationMethod = "SqlLogin"
	AuthAzureAD         AuthenticationMethod = "AzureAD"
	AuthManagedIdentity AuthenticationMethod = "ManagedIdentity"
)

// AzureADAuth defines Azure AD / Entra ID authentication parameters.
type AzureADAuth struct {
	// ClientID is the Azure AD application (client) ID.
	// +kubebuilder:validation:MinLength=1
	ClientID string `json:"clientID"`

	// TenantID is the Azure AD tenant ID.
	// +kubebuilder:validation:MinLength=1
	TenantID string `json:"tenantID"`

	// ClientSecretRef references a Secret containing the "clientSecret" key.
	// +optional
	ClientSecretRef *CrossNamespaceSecretReference `json:"clientSecretRef,omitempty"`
}

// ManagedIdentityAuth defines Azure Managed Identity authentication parameters.
type ManagedIdentityAuth struct {
	// ClientID is the user-assigned managed identity client ID.
	// Leave empty for system-assigned identity.
	// +optional
	ClientID *string `json:"clientID,omitempty"`
}

// CrossNamespaceSecretReference references a Secret that may live in another namespace.
type CrossNamespaceSecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the Secret. If empty, defaults to the namespace of the referencing resource.
	// +optional
	Namespace *string `json:"namespace,omitempty"`
}

// InstanceSpec defines parameters for an operator-managed SQL Server deployment.
type InstanceSpec struct {
	// Image is the SQL Server container image.
	// +optional
	// +kubebuilder:default="mcr.microsoft.com/mssql/server:2022-latest"
	Image *string `json:"image,omitempty"`

	// SAPasswordSecret references a Secret containing the "MSSQL_SA_PASSWORD" key.
	SAPasswordSecret SecretReference `json:"saPasswordSecret"`

	// AcceptEULA must be true to accept the SQL Server EULA.
	AcceptEULA bool `json:"acceptEULA"`

	// Edition is the SQL Server PID (Developer, Express, Standard, Enterprise).
	// +optional
	// +kubebuilder:default="Developer"
	// +kubebuilder:validation:Enum=Developer;Express;Standard;Enterprise;EnterpriseCore
	Edition *string `json:"edition,omitempty"`

	// Replicas is the number of SQL Server pods. 1 = standalone, 2+ = AG cluster.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Replicas *int32 `json:"replicas,omitempty"`

	// StorageSize is the PVC size for /var/opt/mssql per replica (e.g. "10Gi").
	// +optional
	// +kubebuilder:default="10Gi"
	StorageSize *string `json:"storageSize,omitempty"`

	// StorageClassName is the StorageClass name for PVCs.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Resources defines CPU/memory requests and limits for the SQL Server container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceType is the Kubernetes Service type for client access (ClusterIP, NodePort, LoadBalancer).
	// +optional
	// +kubebuilder:default="ClusterIP"
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	ServiceType *corev1.ServiceType `json:"serviceType,omitempty"`

	// NodeSelector for scheduling SQL Server pods on specific nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for SQL Server pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity rules for SQL Server pods.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// TopologySpreadConstraints for SQL Server pods.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// Certificates configures TLS certificate management for HADR endpoints.
	// Required when replicas > 1. Ignored when replicas == 1.
	// +optional
	Certificates *CertificateSpec `json:"certificates,omitempty"`

	// AvailabilityGroup configures the Always On AG for multi-replica deployments.
	// Ignored when replicas == 1.
	// +optional
	AvailabilityGroup *ManagedAGSpec `json:"availabilityGroup,omitempty"`
}

// CertificateMode defines how HADR certificates are managed.
// +kubebuilder:validation:Enum=SelfSigned;CertManager
type CertificateMode string

const (
	CertificateModeSelfSigned  CertificateMode = "SelfSigned"
	CertificateModeCertManager CertificateMode = "CertManager"
)

// CertificateSpec configures certificate management for HADR endpoints.
type CertificateSpec struct {
	// Mode determines how certificates are managed: SelfSigned (operator generates) or CertManager.
	// +optional
	// +kubebuilder:default="SelfSigned"
	Mode *CertificateMode `json:"mode,omitempty"`

	// IssuerRef references a cert-manager Issuer or ClusterIssuer. Required when mode is CertManager.
	// +optional
	IssuerRef *CertManagerIssuerRef `json:"issuerRef,omitempty"`

	// Duration is the certificate validity duration.
	// +optional
	// +kubebuilder:default="8760h"
	Duration *string `json:"duration,omitempty"`

	// RenewBefore is how long before expiry to renew certificates.
	// +optional
	// +kubebuilder:default="720h"
	RenewBefore *string `json:"renewBefore,omitempty"`
}

// CertManagerIssuerRef references a cert-manager issuer.
type CertManagerIssuerRef struct {
	// Name of the Issuer or ClusterIssuer.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind is Issuer or ClusterIssuer.
	// +optional
	// +kubebuilder:default="ClusterIssuer"
	// +kubebuilder:validation:Enum=Issuer;ClusterIssuer
	Kind *string `json:"kind,omitempty"`

	// Group is the API group, defaults to cert-manager.io.
	// +optional
	// +kubebuilder:default="cert-manager.io"
	Group *string `json:"group,omitempty"`
}

// ManagedAGSpec configures the Availability Group created by the operator for multi-replica deployments.
type ManagedAGSpec struct {
	// AGName is the name of the Availability Group on SQL Server. Defaults to "{cr.name}-ag".
	// +optional
	AGName *string `json:"agName,omitempty"`

	// AvailabilityMode for all replicas: SynchronousCommit or AsynchronousCommit.
	// +optional
	// +kubebuilder:default="SynchronousCommit"
	// +kubebuilder:validation:Enum=SynchronousCommit;AsynchronousCommit
	AvailabilityMode *string `json:"availabilityMode,omitempty"`

	// AutoFailover enables operator-managed automatic failover.
	// +optional
	// +kubebuilder:default=true
	AutoFailover *bool `json:"autoFailover,omitempty"`

	// HealthCheckInterval is how often to check primary health (e.g. "10s").
	// +optional
	// +kubebuilder:default="10s"
	HealthCheckInterval *string `json:"healthCheckInterval,omitempty"`

	// FailoverCooldown is the minimum time between auto-failovers (e.g. "60s").
	// +optional
	// +kubebuilder:default="60s"
	FailoverCooldown *string `json:"failoverCooldown,omitempty"`

	// Databases to include in the Availability Group.
	// +optional
	Databases []string `json:"databases,omitempty"`
}

// SQLServerSpec defines the desired state of a shared SQL Server connection reference.
type SQLServerSpec struct {
	// Host is the hostname or IP of the SQL Server instance.
	// Required when instance is not set (external mode).
	// +optional
	Host string `json:"host,omitempty"`

	// Port is the port number. Defaults to 1433.
	// +optional
	// +kubebuilder:default=1433
	Port *int32 `json:"port,omitempty"`

	// AuthMethod defines the authentication method. Defaults to SqlLogin.
	// +optional
	// +kubebuilder:default=SqlLogin
	AuthMethod AuthenticationMethod `json:"authMethod,omitempty"`

	// CredentialsSecret references a Secret containing "username" and "password" keys.
	// Required when authMethod is SqlLogin in external mode.
	// Optional in managed mode: if omitted, the operator uses "sa" with the password from instance.saPasswordSecret.
	// +optional
	CredentialsSecret *CrossNamespaceSecretReference `json:"credentialsSecret,omitempty"`

	// AzureAD contains Azure AD authentication configuration.
	// Required when authMethod is AzureAD.
	// +optional
	AzureAD *AzureADAuth `json:"azureAD,omitempty"`

	// ManagedIdentity contains Managed Identity authentication configuration.
	// Required when authMethod is ManagedIdentity.
	// +optional
	ManagedIdentity *ManagedIdentityAuth `json:"managedIdentity,omitempty"`

	// TLS enables TLS encryption for the SQL Server connection.
	// +optional
	// +kubebuilder:default=false
	TLS *bool `json:"tls,omitempty"`

	// MaxConnections is the maximum number of connections in the pool for this server.
	// +optional
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxConnections *int32 `json:"maxConnections,omitempty"`

	// ConnectionTimeout is the connection timeout in seconds.
	// +optional
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:validation:Maximum=300
	ConnectionTimeout *int32 `json:"connectionTimeout,omitempty"`

	// Instance configures an operator-managed SQL Server deployment.
	// When set, the operator creates a StatefulSet, Services, and PVCs.
	// When absent, the operator connects to an external SQL Server (host is required).
	// +optional
	Instance *InstanceSpec `json:"instance,omitempty"`
}

// SQLServerStatus defines the observed state of a SQL Server connection.
type SQLServerStatus struct {
	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ServerVersion is the SQL Server version string (e.g. "16.0.4135.4").
	// +optional
	ServerVersion string `json:"serverVersion,omitempty"`

	// Edition is the SQL Server edition (e.g. "Enterprise", "Standard", "Express").
	// +optional
	Edition string `json:"edition,omitempty"`

	// LastConnectedTime is when the operator last successfully connected.
	// +optional
	LastConnectedTime *metav1.Time `json:"lastConnectedTime,omitempty"`

	// Host is the effective hostname for this SQL Server (FQDN of the managed Service, or spec.host for external).
	// +optional
	Host string `json:"host,omitempty"`

	// ReadyReplicas is the number of ready pods (managed mode only).
	// +optional
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`

	// PrimaryReplica is the current primary server name (managed cluster mode only).
	// +optional
	PrimaryReplica string `json:"primaryReplica,omitempty"`

	// CertificatesReady indicates whether HADR certificates are provisioned (managed cluster mode only).
	// +optional
	CertificatesReady *bool `json:"certificatesReady,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mssrv,categories=mssql
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`
// +kubebuilder:printcolumn:name="Port",type=integer,JSONPath=`.spec.port`
// +kubebuilder:printcolumn:name="Auth",type=string,JSONPath=`.spec.authMethod`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.serverVersion`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SQLServer is the Schema for the sqlservers API.
// It defines a shared SQL Server connection that other CRDs can reference via serverRef.
type SQLServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SQLServerSpec   `json:"spec,omitempty"`
	Status SQLServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SQLServerList contains a list of SQLServer.
type SQLServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SQLServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SQLServer{}, &SQLServerList{})
}
