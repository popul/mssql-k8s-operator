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

// MockClient is an in-memory implementation of SQLClient for testing.
type MockClient struct {
	mu           sync.RWMutex
	databases    map[string]*MockDatabase
	logins       map[string]*MockLogin
	users        map[string]*MockUser // key: "dbName/userName"
	calls        map[string]int
	ConnectError error
}

// NewMockClient creates a new MockClient.
func NewMockClient() *MockClient {
	return &MockClient{
		databases: make(map[string]*MockDatabase),
		logins:    make(map[string]*MockLogin),
		users:     make(map[string]*MockUser),
		calls:     make(map[string]int),
	}
}

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
	delete(m.databases, name)
	return nil
}

func (m *MockClient) GetDatabaseOwner(_ context.Context, name string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	db, ok := m.databases[dbName]
	if !ok {
		return fmt.Errorf("database %q not found", dbName)
	}
	db.Owner = owner
	return nil
}

func (m *MockClient) GetDatabaseCollation(_ context.Context, name string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	login, ok := m.logins[name]
	if !ok {
		return fmt.Errorf("login %q not found", name)
	}
	login.Password = password
	return nil
}

func (m *MockClient) GetLoginDefaultDatabase(_ context.Context, name string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	delete(m.users, userKey(dbName, userName))
	return nil
}

func (m *MockClient) GetUserDatabaseRoles(_ context.Context, dbName, userName string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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

// --- Cross-reference checks ---

func (m *MockClient) LoginHasUsers(_ context.Context, loginName string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
