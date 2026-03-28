package sql

import (
	"context"
	"fmt"
	"sync"
)

// MockDatabase represents a database in the mock.
type MockDatabase struct {
	Name      string
	Collation string
	Owner     string
}

// MockLogin represents a login in the mock.
type MockLogin struct {
	Name            string
	Password        string
	DefaultDatabase string
	ServerRoles     []string
}

// MockUser represents a database user in the mock.
type MockUser struct {
	DBName    string
	UserName  string
	LoginName string
	Roles     []string
	OwnsObjects bool
}

// MockSchema represents a schema in the mock.
type MockSchema struct {
	DBName     string
	SchemaName string
	Owner      string
	HasObjects bool
}

// MockPermission represents a permission in the mock.
type MockPermission struct {
	Permission string
	Target     string
	State      string // "GRANT" or "DENY"
}

// MockClient is an in-memory implementation of SQLClient for testing.
type MockClient struct {
	mu           sync.RWMutex
	databases    map[string]*MockDatabase
	logins       map[string]*MockLogin
	users        map[string]*MockUser       // key: "dbName/userName"
	schemas      map[string]*MockSchema     // key: "dbName/schemaName"
	permissions  map[string][]MockPermission // key: "dbName/userName"
	calls        map[string]int
	ConnectError error
	// MethodErrors allows injecting errors for specific methods.
	// Key is the method name (e.g. "CreateDatabase"), value is the error to return.
	MethodErrors map[string]error
}

// NewMockClient creates a new MockClient.
func NewMockClient() *MockClient {
	return &MockClient{
		databases:    make(map[string]*MockDatabase),
		logins:       make(map[string]*MockLogin),
		users:        make(map[string]*MockUser),
		schemas:      make(map[string]*MockSchema),
		permissions:  make(map[string][]MockPermission),
		calls:        make(map[string]int),
		MethodErrors: make(map[string]error),
	}
}

// track must be called while m.mu is held (Lock or RLock promoted to Lock).
// Since all public methods already hold the lock, this is safe.
func (m *MockClient) track(method string) {
	m.calls[method]++
}

// WasCalled returns true if the given method was called at least once.
func (m *MockClient) WasCalled(method string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.calls[method] > 0
}

// CallCount returns the number of times a method was called.
func (m *MockClient) CallCount(method string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.calls[method]
}

// ResetCalls clears the call tracking.
func (m *MockClient) ResetCalls() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = make(map[string]int)
}

// SetUserOwnsObjects configures whether a user owns objects (for testing deletion blocking).
func (m *MockClient) SetUserOwnsObjects(dbName, userName string, owns bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := dbName + "/" + userName
	if u, ok := m.users[key]; ok {
		u.OwnsObjects = owns
	}
}

func (m *MockClient) checkConnect() error {
	if m.ConnectError != nil {
		return m.ConnectError
	}
	return nil
}

// checkMethodError returns injected error for a method, if any.
func (m *MockClient) checkMethodError(method string) error {
	if err, ok := m.MethodErrors[method]; ok {
		return err
	}
	return nil
}

// SetMethodError injects an error for a specific method. Pass nil to clear.
func (m *MockClient) SetMethodError(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.MethodErrors, method)
	} else {
		m.MethodErrors[method] = err
	}
}

func userKey(dbName, userName string) string {
	return dbName + "/" + userName
}

// --- Database operations ---

func (m *MockClient) DatabaseExists(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("DatabaseExists")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	_, ok := m.databases[name]
	return ok, nil
}

func (m *MockClient) CreateDatabase(_ context.Context, name string, collation *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("CreateDatabase")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("CreateDatabase"); err != nil {
		return err
	}
	col := ""
	if collation != nil {
		col = *collation
	}
	m.databases[name] = &MockDatabase{Name: name, Collation: col, Owner: "dbo"}
	return nil
}

func (m *MockClient) DropDatabase(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("DropDatabase")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("DropDatabase"); err != nil {
		return err
	}
	delete(m.databases, name)
	return nil
}

func (m *MockClient) GetDatabaseOwner(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetDatabaseOwner")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	db, ok := m.databases[name]
	if !ok {
		return "", fmt.Errorf("database %q not found", name)
	}
	return db.Owner, nil
}

func (m *MockClient) SetDatabaseOwner(_ context.Context, dbName, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SetDatabaseOwner")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("SetDatabaseOwner"); err != nil {
		return err
	}
	db, ok := m.databases[dbName]
	if !ok {
		return fmt.Errorf("database %q not found", dbName)
	}
	db.Owner = owner
	return nil
}

func (m *MockClient) GetDatabaseCollation(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetDatabaseCollation")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	db, ok := m.databases[name]
	if !ok {
		return "", fmt.Errorf("database %q not found", name)
	}
	return db.Collation, nil
}

// SetDatabaseCollation allows tests to simulate collation drift.
func (m *MockClient) SetDatabaseCollation(_ context.Context, name, collation string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if db, ok := m.databases[name]; ok {
		db.Collation = collation
	}
}

// --- Login operations ---

func (m *MockClient) LoginExists(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("LoginExists")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	_, ok := m.logins[name]
	return ok, nil
}

func (m *MockClient) CreateLogin(_ context.Context, name, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("CreateLogin")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("CreateLogin"); err != nil {
		return err
	}
	m.logins[name] = &MockLogin{Name: name, Password: password}
	return nil
}

func (m *MockClient) DropLogin(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("DropLogin")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("DropLogin"); err != nil {
		return err
	}
	delete(m.logins, name)
	return nil
}

func (m *MockClient) UpdateLoginPassword(_ context.Context, name, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("UpdateLoginPassword")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("UpdateLoginPassword"); err != nil {
		return err
	}
	login, ok := m.logins[name]
	if !ok {
		return fmt.Errorf("login %q not found", name)
	}
	login.Password = password
	return nil
}

func (m *MockClient) GetLoginDefaultDatabase(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetLoginDefaultDatabase")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	login, ok := m.logins[name]
	if !ok {
		return "", fmt.Errorf("login %q not found", name)
	}
	return login.DefaultDatabase, nil
}

func (m *MockClient) SetLoginDefaultDatabase(_ context.Context, name, dbName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SetLoginDefaultDatabase")
	if err := m.checkConnect(); err != nil {
		return err
	}
	login, ok := m.logins[name]
	if !ok {
		return fmt.Errorf("login %q not found", name)
	}
	login.DefaultDatabase = dbName
	return nil
}

func (m *MockClient) GetLoginServerRoles(_ context.Context, name string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetLoginServerRoles")
	if err := m.checkConnect(); err != nil {
		return nil, err
	}
	login, ok := m.logins[name]
	if !ok {
		return nil, fmt.Errorf("login %q not found", name)
	}
	result := make([]string, len(login.ServerRoles))
	copy(result, login.ServerRoles)
	return result, nil
}

func (m *MockClient) AddLoginToServerRole(_ context.Context, login, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("AddLoginToServerRole")
	if err := m.checkConnect(); err != nil {
		return err
	}
	l, ok := m.logins[login]
	if !ok {
		return fmt.Errorf("login %q not found", login)
	}
	for _, r := range l.ServerRoles {
		if r == role {
			return nil // already has role
		}
	}
	l.ServerRoles = append(l.ServerRoles, role)
	return nil
}

func (m *MockClient) RemoveLoginFromServerRole(_ context.Context, login, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("RemoveLoginFromServerRole")
	if err := m.checkConnect(); err != nil {
		return err
	}
	l, ok := m.logins[login]
	if !ok {
		return fmt.Errorf("login %q not found", login)
	}
	filtered := l.ServerRoles[:0]
	for _, r := range l.ServerRoles {
		if r != role {
			filtered = append(filtered, r)
		}
	}
	l.ServerRoles = filtered
	return nil
}

// --- DatabaseUser operations ---

func (m *MockClient) UserExists(_ context.Context, dbName, userName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("UserExists")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	_, ok := m.users[userKey(dbName, userName)]
	return ok, nil
}

func (m *MockClient) CreateUser(_ context.Context, dbName, userName, loginName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("CreateUser")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("CreateUser"); err != nil {
		return err
	}
	m.users[userKey(dbName, userName)] = &MockUser{
		DBName:    dbName,
		UserName:  userName,
		LoginName: loginName,
	}
	return nil
}

func (m *MockClient) DropUser(_ context.Context, dbName, userName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("DropUser")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("DropUser"); err != nil {
		return err
	}
	delete(m.users, userKey(dbName, userName))
	return nil
}

func (m *MockClient) GetUserDatabaseRoles(_ context.Context, dbName, userName string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetUserDatabaseRoles")
	if err := m.checkConnect(); err != nil {
		return nil, err
	}
	u, ok := m.users[userKey(dbName, userName)]
	if !ok {
		return nil, fmt.Errorf("user %q not found in database %q", userName, dbName)
	}
	result := make([]string, len(u.Roles))
	copy(result, u.Roles)
	return result, nil
}

func (m *MockClient) AddUserToDatabaseRole(_ context.Context, dbName, userName, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("AddUserToDatabaseRole")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("AddUserToDatabaseRole"); err != nil {
		return err
	}
	u, ok := m.users[userKey(dbName, userName)]
	if !ok {
		return fmt.Errorf("user %q not found in database %q", userName, dbName)
	}
	for _, r := range u.Roles {
		if r == role {
			return nil
		}
	}
	u.Roles = append(u.Roles, role)
	return nil
}

func (m *MockClient) RemoveUserFromDatabaseRole(_ context.Context, dbName, userName, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("RemoveUserFromDatabaseRole")
	if err := m.checkConnect(); err != nil {
		return err
	}
	u, ok := m.users[userKey(dbName, userName)]
	if !ok {
		return fmt.Errorf("user %q not found in database %q", userName, dbName)
	}
	filtered := u.Roles[:0]
	for _, r := range u.Roles {
		if r != role {
			filtered = append(filtered, r)
		}
	}
	u.Roles = filtered
	return nil
}

func (m *MockClient) UserOwnsObjects(_ context.Context, dbName, userName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("UserOwnsObjects")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	u, ok := m.users[userKey(dbName, userName)]
	if !ok {
		return false, nil
	}
	return u.OwnsObjects, nil
}

// GetMockUser returns the internal MockUser for direct inspection in tests.
func (m *MockClient) GetMockUser(dbName, userName string) *MockUser {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.users[userKey(dbName, userName)]
}

// --- Schema operations ---

func schemaKey(dbName, schemaName string) string {
	return dbName + "/" + schemaName
}

func (m *MockClient) SchemaExists(_ context.Context, dbName, schemaName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SchemaExists")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	_, ok := m.schemas[schemaKey(dbName, schemaName)]
	return ok, nil
}

func (m *MockClient) CreateSchema(_ context.Context, dbName, schemaName string, owner *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("CreateSchema")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("CreateSchema"); err != nil {
		return err
	}
	o := "dbo"
	if owner != nil && *owner != "" {
		o = *owner
	}
	m.schemas[schemaKey(dbName, schemaName)] = &MockSchema{
		DBName: dbName, SchemaName: schemaName, Owner: o,
	}
	return nil
}

func (m *MockClient) DropSchema(_ context.Context, dbName, schemaName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("DropSchema")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("DropSchema"); err != nil {
		return err
	}
	delete(m.schemas, schemaKey(dbName, schemaName))
	return nil
}

func (m *MockClient) GetSchemaOwner(_ context.Context, dbName, schemaName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetSchemaOwner")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	if err := m.checkMethodError("GetSchemaOwner"); err != nil {
		return "", err
	}
	s, ok := m.schemas[schemaKey(dbName, schemaName)]
	if !ok {
		return "", fmt.Errorf("schema %q not found in database %q", schemaName, dbName)
	}
	return s.Owner, nil
}

func (m *MockClient) SetSchemaOwner(_ context.Context, dbName, schemaName, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SetSchemaOwner")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("SetSchemaOwner"); err != nil {
		return err
	}
	s, ok := m.schemas[schemaKey(dbName, schemaName)]
	if !ok {
		return fmt.Errorf("schema %q not found in database %q", schemaName, dbName)
	}
	s.Owner = owner
	return nil
}

func (m *MockClient) SchemaHasObjects(_ context.Context, dbName, schemaName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SchemaHasObjects")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	s, ok := m.schemas[schemaKey(dbName, schemaName)]
	if !ok {
		return false, nil
	}
	return s.HasObjects, nil
}

// SetSchemaHasObjects configures whether a schema has objects (for testing deletion blocking).
func (m *MockClient) SetSchemaHasObjects(dbName, schemaName string, has bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := schemaKey(dbName, schemaName)
	if s, ok := m.schemas[key]; ok {
		s.HasObjects = has
	}
}

// --- Permission operations ---

func permKey(dbName, userName string) string {
	return dbName + "/" + userName
}

func (m *MockClient) GetPermissions(_ context.Context, dbName, userName string) ([]PermissionState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetPermissions")
	if err := m.checkConnect(); err != nil {
		return nil, err
	}
	perms := m.permissions[permKey(dbName, userName)]
	result := make([]PermissionState, len(perms))
	for i, p := range perms {
		result[i] = PermissionState{Permission: p.Permission, Target: p.Target, State: p.State}
	}
	return result, nil
}

func (m *MockClient) GrantPermission(_ context.Context, dbName, permission, target, userName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GrantPermission")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("GrantPermission"); err != nil {
		return err
	}
	key := permKey(dbName, userName)
	// Remove any existing entry for this permission+target
	m.removePermLocked(key, permission, target)
	m.permissions[key] = append(m.permissions[key], MockPermission{
		Permission: permission, Target: target, State: "GRANT",
	})
	return nil
}

func (m *MockClient) DenyPermission(_ context.Context, dbName, permission, target, userName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("DenyPermission")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("DenyPermission"); err != nil {
		return err
	}
	key := permKey(dbName, userName)
	m.removePermLocked(key, permission, target)
	m.permissions[key] = append(m.permissions[key], MockPermission{
		Permission: permission, Target: target, State: "DENY",
	})
	return nil
}

func (m *MockClient) RevokePermission(_ context.Context, dbName, permission, target, userName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("RevokePermission")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("RevokePermission"); err != nil {
		return err
	}
	key := permKey(dbName, userName)
	m.removePermLocked(key, permission, target)
	return nil
}

// removePermLocked removes a permission entry (must be called with mu held).
func (m *MockClient) removePermLocked(key, permission, target string) {
	perms := m.permissions[key]
	filtered := perms[:0]
	for _, p := range perms {
		if !(p.Permission == permission && p.Target == target) {
			filtered = append(filtered, p)
		}
	}
	m.permissions[key] = filtered
}

// --- Cross-reference checks ---

func (m *MockClient) LoginHasUsers(_ context.Context, loginName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("LoginHasUsers")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	for _, u := range m.users {
		if u.LoginName == loginName {
			return true, nil
		}
	}
	return false, nil
}

// --- Backup/Restore operations ---

func (m *MockClient) BackupDatabase(_ context.Context, dbName, destination string, backupType string, compression bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("BackupDatabase")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("BackupDatabase"); err != nil {
		return err
	}
	return nil
}

func (m *MockClient) RestoreDatabase(_ context.Context, dbName, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("RestoreDatabase")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("RestoreDatabase"); err != nil {
		return err
	}
	return nil
}

// --- Connection ---

func (m *MockClient) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("Close")
	return nil
}

func (m *MockClient) Ping(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("Ping")
	return m.checkConnect()
}
