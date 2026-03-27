package v1alpha1

// ServerReference defines how to connect to a SQL Server instance.
type ServerReference struct {
	// Host is the hostname or IP of the SQL Server instance.
	Host string `json:"host"`

	// Port is the port number. Defaults to 1433.
	// +optional
	// +kubebuilder:default=1433
	Port *int32 `json:"port,omitempty"`

	// CredentialsSecret references a Secret containing "username" and "password" keys.
	CredentialsSecret SecretReference `json:"credentialsSecret"`

	// TLS enables TLS encryption for the SQL Server connection.
	// +optional
	// +kubebuilder:default=false
	TLS *bool `json:"tls,omitempty"`
}

// SecretReference is a reference to a Kubernetes Secret by name (same namespace).
type SecretReference struct {
	// Name of the Secret.
	Name string `json:"name"`
}

// LoginReference is a reference to a Login CR by name (same namespace).
type LoginReference struct {
	// Name of the Login CR.
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
	ReasonReady                     = "Ready"
	ReasonConnectionFailed          = "ConnectionFailed"
	ReasonSecretNotFound            = "SecretNotFound"
	ReasonInvalidCredentialsSecret  = "InvalidCredentialsSecret"
	ReasonImmutableFieldChanged     = "ImmutableFieldChanged"
	ReasonCollationChangeNotSupported = "CollationChangeNotSupported"
	ReasonLoginInUse                = "LoginInUse"
	ReasonLoginRefNotFound          = "LoginRefNotFound"
	ReasonLoginNotReady             = "LoginNotReady"
	ReasonUserOwnsObjects           = "UserOwnsObjects"
	ReasonInvalidServerRole         = "InvalidServerRole"
	ReasonDatabaseProvisioning      = "DatabaseProvisioning"
)

// Finalizer name used by all CRDs in this operator.
const Finalizer = "mssql.popul.io/finalizer"
