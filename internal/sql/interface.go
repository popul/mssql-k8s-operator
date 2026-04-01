package sql

import "context"

// SQLClient defines the interface for all SQL Server operations.
// Implementations must be safe for concurrent use.
type SQLClient interface {
	// Database operations
	DatabaseExists(ctx context.Context, name string) (bool, error)
	CreateDatabase(ctx context.Context, name string, collation *string) error
	DropDatabase(ctx context.Context, name string) error
	GetDatabaseOwner(ctx context.Context, name string) (string, error)
	SetDatabaseOwner(ctx context.Context, dbName, owner string) error
	GetDatabaseCollation(ctx context.Context, name string) (string, error)

	// Login operations
	LoginExists(ctx context.Context, name string) (bool, error)
	CreateLogin(ctx context.Context, name, password string) error
	DropLogin(ctx context.Context, name string) error
	UpdateLoginPassword(ctx context.Context, name, password string) error
	GetLoginDefaultDatabase(ctx context.Context, name string) (string, error)
	SetLoginDefaultDatabase(ctx context.Context, name, dbName string) error
	GetLoginServerRoles(ctx context.Context, name string) ([]string, error)
	AddLoginToServerRole(ctx context.Context, login, role string) error
	RemoveLoginFromServerRole(ctx context.Context, login, role string) error

	// DatabaseUser operations
	UserExists(ctx context.Context, dbName, userName string) (bool, error)
	CreateUser(ctx context.Context, dbName, userName, loginName string) error
	DropUser(ctx context.Context, dbName, userName string) error
	GetUserDatabaseRoles(ctx context.Context, dbName, userName string) ([]string, error)
	AddUserToDatabaseRole(ctx context.Context, dbName, userName, role string) error
	RemoveUserFromDatabaseRole(ctx context.Context, dbName, userName, role string) error
	UserOwnsObjects(ctx context.Context, dbName, userName string) (bool, error)

	// Schema operations
	SchemaExists(ctx context.Context, dbName, schemaName string) (bool, error)
	CreateSchema(ctx context.Context, dbName, schemaName string, owner *string) error
	DropSchema(ctx context.Context, dbName, schemaName string) error
	GetSchemaOwner(ctx context.Context, dbName, schemaName string) (string, error)
	SetSchemaOwner(ctx context.Context, dbName, schemaName, owner string) error
	SchemaHasObjects(ctx context.Context, dbName, schemaName string) (bool, error)

	// Permission operations
	GetPermissions(ctx context.Context, dbName, userName string) ([]PermissionState, error)
	GrantPermission(ctx context.Context, dbName, permission, target, userName string) error
	DenyPermission(ctx context.Context, dbName, permission, target, userName string) error
	RevokePermission(ctx context.Context, dbName, permission, target, userName string) error

	// Cross-reference checks
	LoginHasUsers(ctx context.Context, loginName string) (bool, error)

	// Backup/Restore operations
	BackupDatabase(ctx context.Context, dbName, destination, backupType string, compression bool) error
	RestoreDatabase(ctx context.Context, dbName, source string) error

	// Availability Group operations
	AGExists(ctx context.Context, agName string) (bool, error)
	CreateAG(ctx context.Context, config *AGConfig) error
	GetAGStatus(ctx context.Context, agName string) (*AGStatus, error)
	AddDatabaseToAG(ctx context.Context, agName, dbName string) error
	RemoveDatabaseFromAG(ctx context.Context, agName, dbName string) error
	JoinAG(ctx context.Context, agName, clusterType string) error
	GrantAGCreateDatabase(ctx context.Context, agName string) error
	AddListenerToAG(ctx context.Context, agName string, listener AGListenerConfig) error
	DropAG(ctx context.Context, agName string) error
	FailoverAG(ctx context.Context, agName string) error
	ForceFailoverAG(ctx context.Context, agName string) error
	GetAGReplicaRole(ctx context.Context, agName, serverName string) (string, error)
	CreateHADREndpoint(ctx context.Context, port int) error
	HADREndpointExists(ctx context.Context) (bool, error)

	// Certificate and endpoint operations for HADR
	CreateMasterKey(ctx context.Context, password string) error
	MasterKeyExists(ctx context.Context) (bool, error)
	CreateCertificate(ctx context.Context, certName, subject string, expiryDate string) error
	CertificateExists(ctx context.Context, certName string) (bool, error)
	BackupCertificate(ctx context.Context, certName, certPath, keyPath, encryptionPassword string) error
	CreateCertificateFromBackup(ctx context.Context, certName, certPath, keyPath, decryptionPassword string) error
	GetCertificateBinary(ctx context.Context, certName string) ([]byte, error)
	CreateCertificateFromBinary(ctx context.Context, certName string, certDER []byte) error
	CreateLoginFromCertificate(ctx context.Context, loginName, certName string) error
	GrantEndpointConnect(ctx context.Context, endpointName, loginName string) error
	CreateHADREndpointWithCert(ctx context.Context, port int, certName string) error

	// Server info
	GetServerVersion(ctx context.Context) (string, error)
	GetServerEdition(ctx context.Context) (string, error)

	// Database configuration
	GetDatabaseRecoveryModel(ctx context.Context, name string) (string, error)
	SetDatabaseRecoveryModel(ctx context.Context, name, model string) error
	GetDatabaseCompatibilityLevel(ctx context.Context, name string) (int, error)
	SetDatabaseCompatibilityLevel(ctx context.Context, name string, level int) error
	GetDatabaseOption(ctx context.Context, name, option string) (bool, error)
	SetDatabaseOption(ctx context.Context, name, option string, value bool) error

	// Point-in-Time Restore
	RestoreDatabasePIT(ctx context.Context, dbName, fullSource, logSource, stopAt string) error
	RestoreDatabaseWithMove(ctx context.Context, dbName, source string, withMove map[string]string) error

	// Connection
	Close() error
	Ping(ctx context.Context) error
}

// AGConfig contains the configuration for creating an Availability Group.
type AGConfig struct {
	Name                      string
	Replicas                  []AGReplicaConfig
	Databases                 []string
	AutomatedBackupPreference string
	DBFailover                bool
	ClusterType               string // "WSFC", "EXTERNAL", "NONE"
}

// AGReplicaConfig contains the configuration for a single AG replica.
type AGReplicaConfig struct {
	ServerName       string
	EndpointURL      string
	AvailabilityMode string // "SYNCHRONOUS_COMMIT" or "ASYNCHRONOUS_COMMIT"
	FailoverMode     string // "AUTOMATIC" or "MANUAL"
	SeedingMode      string // "AUTOMATIC" or "MANUAL"
	SecondaryRole    string // "ALL", "READ_ONLY", "NO"
}

// AGListenerConfig contains the configuration for an AG listener.
type AGListenerConfig struct {
	Name        string
	Port        int
	IPAddresses []AGListenerIPConfig
}

// AGListenerIPConfig contains a listener IP address with subnet mask.
type AGListenerIPConfig struct {
	IP         string
	SubnetMask string
}

// AGStatus represents the observed state of an Availability Group.
type AGStatus struct {
	Name           string
	PrimaryReplica string
	Replicas       []AGReplicaState
	Databases      []AGDatabaseState
}

// AGReplicaState represents the observed state of a replica.
type AGReplicaState struct {
	ServerName           string
	Role                 string // "PRIMARY", "SECONDARY", "RESOLVING"
	SynchronizationState string // "SYNCHRONIZED", "SYNCHRONIZING", "NOT_SYNCHRONIZING"
	Connected            bool
}

// AGDatabaseState represents the observed state of a database in the AG.
type AGDatabaseState struct {
	Name                 string
	SynchronizationState string // "SYNCHRONIZED", "SYNCHRONIZING", "NOT_SYNCHRONIZING"
	Joined               bool
}

// PermissionState represents a permission as observed on SQL Server.
type PermissionState struct {
	Permission string
	Target     string
	State      string // "GRANT" or "DENY"
}

// ClientFactory creates a SQLClient for the given connection parameters.
type ClientFactory func(host string, port int, username, password string, tlsEnabled bool) (SQLClient, error)

// AzureADClientFactory creates a SQLClient using Azure AD token-based authentication.
type AzureADClientFactory func(host string, port int, clientID, tenantID, clientSecret string, tlsEnabled bool) (SQLClient, error)

// ManagedIdentityClientFactory creates a SQLClient using Azure Managed Identity.
type ManagedIdentityClientFactory func(host string, port int, clientID string, tlsEnabled bool) (SQLClient, error)
