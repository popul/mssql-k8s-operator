package sql

import (
	"context"
	"testing"
)

// TestSQLClientInterfaceCompleteness verifies that the interface has all expected methods
// by checking that a mock implementation satisfies it at compile time.
func TestSQLClientInterfaceCompleteness(t *testing.T) {
	// This is a compile-time check. If SQLClient is missing a method,
	// this file won't compile.
	var _ SQLClient = (*interfaceChecker)(nil)
}

// interfaceChecker is a minimal implementation that verifies the interface shape.
type interfaceChecker struct{}

func (c *interfaceChecker) DatabaseExists(ctx context.Context, name string) (bool, error) {
	return false, nil
}
func (c *interfaceChecker) CreateDatabase(ctx context.Context, name string, collation *string) error {
	return nil
}
func (c *interfaceChecker) DropDatabase(ctx context.Context, name string) error {
	return nil
}
func (c *interfaceChecker) GetDatabaseOwner(ctx context.Context, name string) (string, error) {
	return "", nil
}
func (c *interfaceChecker) SetDatabaseOwner(ctx context.Context, dbName, owner string) error {
	return nil
}
func (c *interfaceChecker) GetDatabaseCollation(ctx context.Context, name string) (string, error) {
	return "", nil
}

func (c *interfaceChecker) LoginExists(ctx context.Context, name string) (bool, error) {
	return false, nil
}
func (c *interfaceChecker) CreateLogin(ctx context.Context, name, password string) error {
	return nil
}
func (c *interfaceChecker) DropLogin(ctx context.Context, name string) error {
	return nil
}
func (c *interfaceChecker) UpdateLoginPassword(ctx context.Context, name, password string) error {
	return nil
}
func (c *interfaceChecker) GetLoginDefaultDatabase(ctx context.Context, name string) (string, error) {
	return "", nil
}
func (c *interfaceChecker) SetLoginDefaultDatabase(ctx context.Context, name, dbName string) error {
	return nil
}
func (c *interfaceChecker) GetLoginServerRoles(ctx context.Context, name string) ([]string, error) {
	return nil, nil
}
func (c *interfaceChecker) AddLoginToServerRole(ctx context.Context, login, role string) error {
	return nil
}
func (c *interfaceChecker) RemoveLoginFromServerRole(ctx context.Context, login, role string) error {
	return nil
}

func (c *interfaceChecker) UserExists(ctx context.Context, dbName, userName string) (bool, error) {
	return false, nil
}
func (c *interfaceChecker) CreateUser(ctx context.Context, dbName, userName, loginName string) error {
	return nil
}
func (c *interfaceChecker) DropUser(ctx context.Context, dbName, userName string) error {
	return nil
}
func (c *interfaceChecker) GetUserDatabaseRoles(ctx context.Context, dbName, userName string) ([]string, error) {
	return nil, nil
}
func (c *interfaceChecker) AddUserToDatabaseRole(ctx context.Context, dbName, userName, role string) error {
	return nil
}
func (c *interfaceChecker) RemoveUserFromDatabaseRole(ctx context.Context, dbName, userName, role string) error {
	return nil
}
func (c *interfaceChecker) UserOwnsObjects(ctx context.Context, dbName, userName string) (bool, error) {
	return false, nil
}
func (c *interfaceChecker) LoginHasUsers(ctx context.Context, loginName string) (bool, error) {
	return false, nil
}

func (c *interfaceChecker) SchemaExists(ctx context.Context, dbName, schemaName string) (bool, error) {
	return false, nil
}
func (c *interfaceChecker) CreateSchema(ctx context.Context, dbName, schemaName string, owner *string) error {
	return nil
}
func (c *interfaceChecker) DropSchema(ctx context.Context, dbName, schemaName string) error {
	return nil
}
func (c *interfaceChecker) GetSchemaOwner(ctx context.Context, dbName, schemaName string) (string, error) {
	return "", nil
}
func (c *interfaceChecker) SetSchemaOwner(ctx context.Context, dbName, schemaName, owner string) error {
	return nil
}
func (c *interfaceChecker) SchemaHasObjects(ctx context.Context, dbName, schemaName string) (bool, error) {
	return false, nil
}

func (c *interfaceChecker) GetPermissions(ctx context.Context, dbName, userName string) ([]PermissionState, error) {
	return nil, nil
}
func (c *interfaceChecker) GrantPermission(ctx context.Context, dbName, permission, target, userName string) error {
	return nil
}
func (c *interfaceChecker) DenyPermission(ctx context.Context, dbName, permission, target, userName string) error {
	return nil
}
func (c *interfaceChecker) RevokePermission(ctx context.Context, dbName, permission, target, userName string) error {
	return nil
}

func (c *interfaceChecker) BackupDatabase(ctx context.Context, dbName, destination string, backupType string, compression bool) error {
	return nil
}
func (c *interfaceChecker) RestoreDatabase(ctx context.Context, dbName, source string) error {
	return nil
}

func (c *interfaceChecker) AGExists(ctx context.Context, agName string) (bool, error) {
	return false, nil
}
func (c *interfaceChecker) CreateAG(ctx context.Context, config AGConfig) error { return nil }
func (c *interfaceChecker) GetAGStatus(ctx context.Context, agName string) (*AGStatus, error) {
	return nil, nil
}
func (c *interfaceChecker) AddDatabaseToAG(ctx context.Context, agName, dbName string) error {
	return nil
}
func (c *interfaceChecker) RemoveDatabaseFromAG(ctx context.Context, agName, dbName string) error {
	return nil
}
func (c *interfaceChecker) JoinAG(ctx context.Context, agName string) error     { return nil }
func (c *interfaceChecker) GrantAGCreateDatabase(ctx context.Context, agName string) error {
	return nil
}
func (c *interfaceChecker) AddListenerToAG(ctx context.Context, agName string, listener AGListenerConfig) error {
	return nil
}
func (c *interfaceChecker) DropAG(ctx context.Context, agName string) error     { return nil }
func (c *interfaceChecker) CreateHADREndpoint(ctx context.Context, port int) error { return nil }
func (c *interfaceChecker) HADREndpointExists(ctx context.Context) (bool, error) {
	return false, nil
}

func (c *interfaceChecker) Close() error                   { return nil }
func (c *interfaceChecker) Ping(ctx context.Context) error { return nil }
