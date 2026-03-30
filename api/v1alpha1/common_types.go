package v1alpha1

// ServerReference defines how to connect to a SQL Server instance.
// Use either inline connection details (host/credentialsSecret) OR a reference to a SQLServer CR (sqlServerRef).
type ServerReference struct {
	// Host is the hostname or IP of the SQL Server instance.
	// Required when sqlServerRef is not set.
	// +optional
	Host string `json:"host,omitempty"`

	// Port is the port number. Defaults to 1433.
	// +optional
	// +kubebuilder:default=1433
	Port *int32 `json:"port,omitempty"`

	// CredentialsSecret references a Secret containing "username" and "password" keys.
	// Required when sqlServerRef is not set and authMethod is SqlLogin.
	// +optional
	CredentialsSecret SecretReference `json:"credentialsSecret,omitempty"`

	// TLS enables TLS encryption for the SQL Server connection.
	// +optional
	// +kubebuilder:default=false
	TLS *bool `json:"tls,omitempty"`

	// SQLServerRef references a SQLServer CR by name. When set, host/port/credentials/tls
	// are inherited from the SQLServer CR and should not be specified inline.
	// +optional
	SQLServerRef *string `json:"sqlServerRef,omitempty"`
}

// SecretReference is a reference to a Kubernetes Secret by name (same namespace).
type SecretReference struct {
	// Name of the Secret.
	Name string `json:"name"`
}

// LoginReference is a reference to a Login CR by name (same namespace).
type LoginReference struct {
	// Name of the Login CR.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// DeletionPolicy determines what happens to the SQL Server object when the CR is deleted.
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

const (
	DeletionPolicyDelete DeletionPolicy = "Delete"
	DeletionPolicyRetain DeletionPolicy = "Retain"
)

// Condition type constants.
const (
	ConditionReady = "Ready"
)

// Reason constants for status conditions.
const (
	ReasonReady                       = "Ready"
	ReasonConnectionFailed            = "ConnectionFailed"
	ReasonSecretNotFound              = "SecretNotFound"
	ReasonInvalidCredentialsSecret    = "InvalidCredentialsSecret"
	ReasonImmutableFieldChanged       = "ImmutableFieldChanged"
	ReasonCollationChangeNotSupported = "CollationChangeNotSupported"
	ReasonLoginInUse                  = "LoginInUse"
	ReasonLoginRefNotFound            = "LoginRefNotFound"
	ReasonLoginNotReady               = "LoginNotReady"
	ReasonUserOwnsObjects             = "UserOwnsObjects"
	ReasonInvalidServerRole           = "InvalidServerRole"
	ReasonDatabaseProvisioning        = "DatabaseProvisioning"
	ReasonSchemaNotEmpty              = "SchemaNotEmpty"
	ReasonDeploymentProvisioning      = "DeploymentProvisioning"
	ReasonDeploymentReady             = "DeploymentReady"
	ReasonEULANotAccepted             = "EULANotAccepted"
	ReasonCertificatesProvisioning    = "CertificatesProvisioning"
	ReasonAGProvisioning              = "AGProvisioning"
)

// Finalizer name used by all CRDs in this operator.
const Finalizer = "mssql.popul.io/finalizer"
