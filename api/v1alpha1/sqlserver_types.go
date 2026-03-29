package v1alpha1

import (
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

// SQLServerSpec defines the desired state of a shared SQL Server connection reference.
type SQLServerSpec struct {
	// Host is the hostname or IP of the SQL Server instance.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// Port is the port number. Defaults to 1433.
	// +optional
	// +kubebuilder:default=1433
	Port *int32 `json:"port,omitempty"`

	// AuthMethod defines the authentication method. Defaults to SqlLogin.
	// +optional
	// +kubebuilder:default=SqlLogin
	AuthMethod AuthenticationMethod `json:"authMethod,omitempty"`

	// CredentialsSecret references a Secret containing "username" and "password" keys.
	// Required when authMethod is SqlLogin.
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
