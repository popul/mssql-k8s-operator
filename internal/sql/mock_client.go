package sql

import (
	"context"
	"fmt"
	"sync"
)

// MockDatabase represents a database in the mock.
type MockDatabase struct {
	Name               string
	Collation          string
	Owner              string
	RecoveryModel      string
	CompatibilityLevel int
	Options            map[string]bool
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
	DBName      string
	UserName    string
	LoginName   string
	Roles       []string
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
	users        map[string]*MockUser        // key: "dbName/userName"
	schemas      map[string]*MockSchema      // key: "dbName/schemaName"
	permissions  map[string][]MockPermission // key: "dbName/userName"
	ags          map[string]*MockAG          // key: AG name
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
		result[i] = PermissionState(p)
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

// --- Availability Group operations ---

// MockAG represents an Availability Group in the mock.
type MockAG struct {
	Name           string
	PrimaryReplica string
	Replicas       []AGReplicaState
	Databases      []AGDatabaseState
	HasListener    bool
}

func (m *MockClient) AGExists(_ context.Context, agName string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("AGExists")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	if m.ags == nil {
		return false, nil
	}
	_, ok := m.ags[agName]
	return ok, nil
}

func (m *MockClient) CreateAG(_ context.Context, config *AGConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("CreateAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("CreateAG"); err != nil {
		return err
	}
	if m.ags == nil {
		m.ags = make(map[string]*MockAG)
	}
	replicas := make([]AGReplicaState, len(config.Replicas))
	for i, r := range config.Replicas {
		role := "SECONDARY"
		if i == 0 {
			role = "PRIMARY"
		}
		replicas[i] = AGReplicaState{
			ServerName:           r.ServerName,
			Role:                 role,
			SynchronizationState: "SYNCHRONIZED",
			Connected:            true,
		}
	}
	databases := make([]AGDatabaseState, len(config.Databases))
	for i, db := range config.Databases {
		databases[i] = AGDatabaseState{Name: db, SynchronizationState: "SYNCHRONIZED", Joined: true}
	}
	primary := ""
	if len(config.Replicas) > 0 {
		primary = config.Replicas[0].ServerName
	}
	m.ags[config.Name] = &MockAG{
		Name:           config.Name,
		PrimaryReplica: primary,
		Replicas:       replicas,
		Databases:      databases,
	}
	return nil
}

func (m *MockClient) GetAGStatus(_ context.Context, agName string) (*AGStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetAGStatus")
	if err := m.checkConnect(); err != nil {
		return nil, err
	}
	if err := m.checkMethodError("GetAGStatus"); err != nil {
		return nil, err
	}
	if m.ags == nil {
		return nil, fmt.Errorf("availability group %q not found", agName)
	}
	ag, ok := m.ags[agName]
	if !ok {
		return nil, fmt.Errorf("availability group %q not found", agName)
	}
	return &AGStatus{
		Name:           ag.Name,
		PrimaryReplica: ag.PrimaryReplica,
		Replicas:       ag.Replicas,
		Databases:      ag.Databases,
	}, nil
}

func (m *MockClient) AddDatabaseToAG(_ context.Context, agName, dbName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("AddDatabaseToAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("AddDatabaseToAG"); err != nil {
		return err
	}
	if ag, ok := m.ags[agName]; ok {
		ag.Databases = append(ag.Databases, AGDatabaseState{Name: dbName, SynchronizationState: "SYNCHRONIZED", Joined: true})
	}
	return nil
}

func (m *MockClient) RemoveDatabaseFromAG(_ context.Context, agName, dbName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("RemoveDatabaseFromAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("RemoveDatabaseFromAG"); err != nil {
		return err
	}
	if ag, ok := m.ags[agName]; ok {
		filtered := ag.Databases[:0]
		for _, db := range ag.Databases {
			if db.Name != dbName {
				filtered = append(filtered, db)
			}
		}
		ag.Databases = filtered
	}
	return nil
}

func (m *MockClient) JoinAG(_ context.Context, agName, clusterType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("JoinAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("JoinAG"); err != nil {
		return err
	}
	return nil
}

func (m *MockClient) GrantAGCreateDatabase(_ context.Context, agName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GrantAGCreateDatabase")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("GrantAGCreateDatabase"); err != nil {
		return err
	}
	return nil
}

func (m *MockClient) AddListenerToAG(_ context.Context, agName string, listener AGListenerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("AddListenerToAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("AddListenerToAG"); err != nil {
		return err
	}
	if ag, ok := m.ags[agName]; ok {
		ag.HasListener = true
	}
	return nil
}

func (m *MockClient) FailoverAG(_ context.Context, agName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("FailoverAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("FailoverAG"); err != nil {
		return err
	}
	// Simulate failover by swapping primary
	if ag, ok := m.ags[agName]; ok {
		for i := range ag.Replicas {
			if ag.Replicas[i].Role == "SECONDARY" {
				// Promote the first secondary
				for j := range ag.Replicas {
					if ag.Replicas[j].Role == "PRIMARY" {
						ag.Replicas[j].Role = "SECONDARY"
						break
					}
				}
				ag.Replicas[i].Role = "PRIMARY"
				ag.PrimaryReplica = ag.Replicas[i].ServerName
				break
			}
		}
	}
	return nil
}

func (m *MockClient) ForceFailoverAG(_ context.Context, agName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("ForceFailoverAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("ForceFailoverAG"); err != nil {
		return err
	}
	// Same as FailoverAG in mock
	if ag, ok := m.ags[agName]; ok {
		for i := range ag.Replicas {
			if ag.Replicas[i].Role == "SECONDARY" {
				for j := range ag.Replicas {
					if ag.Replicas[j].Role == "PRIMARY" {
						ag.Replicas[j].Role = "SECONDARY"
						break
					}
				}
				ag.Replicas[i].Role = "PRIMARY"
				ag.PrimaryReplica = ag.Replicas[i].ServerName
				break
			}
		}
	}
	return nil
}

func (m *MockClient) GetAGReplicaRole(_ context.Context, agName, serverName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetAGReplicaRole")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	if err := m.checkMethodError("GetAGReplicaRole"); err != nil {
		return "", err
	}
	if ag, ok := m.ags[agName]; ok {
		for _, r := range ag.Replicas {
			if r.ServerName == serverName {
				return r.Role, nil
			}
		}
	}
	return "", fmt.Errorf("replica %s not found in AG %s", serverName, agName)
}

func (m *MockClient) DropAG(_ context.Context, agName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("DropAG")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("DropAG"); err != nil {
		return err
	}
	delete(m.ags, agName)
	return nil
}

func (m *MockClient) CreateHADREndpoint(_ context.Context, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("CreateHADREndpoint")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("CreateHADREndpoint"); err != nil {
		return err
	}
	return nil
}

func (m *MockClient) HADREndpointExists(_ context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("HADREndpointExists")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	return m.WasCalledLocked("CreateHADREndpoint"), nil
}

// WasCalledLocked checks call history without acquiring lock (caller must hold lock).
func (m *MockClient) WasCalledLocked(method string) bool {
	return m.calls[method] > 0
}

// --- Backup/Restore operations ---

func (m *MockClient) BackupDatabase(_ context.Context, dbName, destination, backupType string, compression bool) error {
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

// --- Server info ---

func (m *MockClient) GetServerVersion(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetServerVersion")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	if err := m.checkMethodError("GetServerVersion"); err != nil {
		return "", err
	}
	return "16.0.4135.4", nil
}

func (m *MockClient) GetServerEdition(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetServerEdition")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	if err := m.checkMethodError("GetServerEdition"); err != nil {
		return "", err
	}
	return "Developer Edition (64-bit)", nil
}

// --- Database configuration ---

func (m *MockClient) GetDatabaseRecoveryModel(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetDatabaseRecoveryModel")
	if err := m.checkConnect(); err != nil {
		return "", err
	}
	if err := m.checkMethodError("GetDatabaseRecoveryModel"); err != nil {
		return "", err
	}
	db, ok := m.databases[name]
	if !ok {
		return "", fmt.Errorf("database %q not found", name)
	}
	if db.RecoveryModel == "" {
		return "FULL", nil
	}
	return db.RecoveryModel, nil
}

func (m *MockClient) SetDatabaseRecoveryModel(_ context.Context, name, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SetDatabaseRecoveryModel")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("SetDatabaseRecoveryModel"); err != nil {
		return err
	}
	db, ok := m.databases[name]
	if !ok {
		return fmt.Errorf("database %q not found", name)
	}
	db.RecoveryModel = model
	return nil
}

func (m *MockClient) GetDatabaseCompatibilityLevel(_ context.Context, name string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetDatabaseCompatibilityLevel")
	if err := m.checkConnect(); err != nil {
		return 0, err
	}
	if err := m.checkMethodError("GetDatabaseCompatibilityLevel"); err != nil {
		return 0, err
	}
	db, ok := m.databases[name]
	if !ok {
		return 0, fmt.Errorf("database %q not found", name)
	}
	if db.CompatibilityLevel == 0 {
		return 160, nil
	}
	return db.CompatibilityLevel, nil
}

func (m *MockClient) SetDatabaseCompatibilityLevel(_ context.Context, name string, level int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SetDatabaseCompatibilityLevel")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("SetDatabaseCompatibilityLevel"); err != nil {
		return err
	}
	db, ok := m.databases[name]
	if !ok {
		return fmt.Errorf("database %q not found", name)
	}
	db.CompatibilityLevel = level
	return nil
}

func (m *MockClient) GetDatabaseOption(_ context.Context, name, option string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("GetDatabaseOption")
	if err := m.checkConnect(); err != nil {
		return false, err
	}
	if err := m.checkMethodError("GetDatabaseOption"); err != nil {
		return false, err
	}
	db, ok := m.databases[name]
	if !ok {
		return false, fmt.Errorf("database %q not found", name)
	}
	if db.Options == nil {
		return false, nil
	}
	return db.Options[option], nil
}

func (m *MockClient) SetDatabaseOption(_ context.Context, name, option string, value bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("SetDatabaseOption")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("SetDatabaseOption"); err != nil {
		return err
	}
	db, ok := m.databases[name]
	if !ok {
		return fmt.Errorf("database %q not found", name)
	}
	if db.Options == nil {
		db.Options = make(map[string]bool)
	}
	db.Options[option] = value
	return nil
}

// --- Point-in-Time Restore ---

func (m *MockClient) RestoreDatabasePIT(_ context.Context, dbName, fullSource, logSource, stopAt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("RestoreDatabasePIT")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("RestoreDatabasePIT"); err != nil {
		return err
	}
	return nil
}

func (m *MockClient) RestoreDatabaseWithMove(_ context.Context, dbName, source string, withMove map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.track("RestoreDatabaseWithMove")
	if err := m.checkConnect(); err != nil {
		return err
	}
	if err := m.checkMethodError("RestoreDatabaseWithMove"); err != nil {
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
