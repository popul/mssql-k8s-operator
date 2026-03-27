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

	// Cross-reference checks
	LoginHasUsers(ctx context.Context, loginName string) (bool, error)

	// Connection
	Close() error
	Ping(ctx context.Context) error
}

// ClientFactory creates a SQLClient for the given connection parameters.
type ClientFactory func(host string, port int, username, password string, tlsEnabled bool) (SQLClient, error)
