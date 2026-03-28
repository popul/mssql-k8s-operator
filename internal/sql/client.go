package sql

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/microsoft/go-mssqldb"
)

// MSSQLClient implements SQLClient using go-mssqldb.
type MSSQLClient struct {
	db *sql.DB
}

func buildConnectionString(host string, port int, username, password string, tlsEnabled bool) string {
	query := url.Values{}
	query.Set("database", "master")
	query.Set("app name", "mssql-k8s-operator")

	encrypt := "disable"
	if tlsEnabled {
		encrypt = "true"
	}
	query.Set("encrypt", encrypt)

	u := &url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(username, password),
		Host:     fmt.Sprintf("%s:%d", host, port),
		RawQuery: query.Encode(),
	}
	return u.String()
}

// NewClientFactory returns a ClientFactory that creates real SQL Server connections.
func NewClientFactory() ClientFactory {
	return func(host string, port int, username, password string, tlsEnabled bool) (SQLClient, error) {
		connStr := buildConnectionString(host, port, username, password, tlsEnabled)
		db, err := sql.Open("sqlserver", connStr)
		if err != nil {
			return nil, fmt.Errorf("failed to open SQL connection: %w", err)
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		return &MSSQLClient{db: db}, nil
	}
}

// --- Connection ---

func (c *MSSQLClient) Close() error {
	return c.db.Close()
}

func (c *MSSQLClient) Ping(ctx context.Context) error {
	return c.db.PingContext(ctx)
}

// --- Database operations ---

func (c *MSSQLClient) DatabaseExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := c.db.QueryRowContext(ctx,
		"SELECT CASE WHEN DB_ID(@p1) IS NOT NULL THEN 1 ELSE 0 END", name).Scan(&exists)
	return exists, err
}

func (c *MSSQLClient) CreateDatabase(ctx context.Context, name string, collation *string) error {
	query := fmt.Sprintf("CREATE DATABASE %s", QuoteName(name))
	if collation != nil && *collation != "" {
		query += fmt.Sprintf(" COLLATE %s", QuoteName(*collation))
	}
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create database %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) DropDatabase(ctx context.Context, name string) error {
	query := fmt.Sprintf("DROP DATABASE IF EXISTS %s", QuoteName(name))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to drop database %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) GetDatabaseOwner(ctx context.Context, name string) (string, error) {
	var owner string
	err := c.db.QueryRowContext(ctx,
		"SELECT SUSER_SNAME(owner_sid) FROM sys.databases WHERE name = @p1", name).Scan(&owner)
	if err != nil {
		return "", fmt.Errorf("failed to get owner for database %s: %w", name, err)
	}
	return owner, nil
}

func (c *MSSQLClient) SetDatabaseOwner(ctx context.Context, dbName, owner string) error {
	query := fmt.Sprintf("ALTER AUTHORIZATION ON DATABASE::%s TO %s", QuoteName(dbName), QuoteName(owner))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to set owner %s on database %s: %w", owner, dbName, err)
	}
	return nil
}

func (c *MSSQLClient) GetDatabaseCollation(ctx context.Context, name string) (string, error) {
	var collation string
	err := c.db.QueryRowContext(ctx,
		"SELECT collation_name FROM sys.databases WHERE name = @p1", name).Scan(&collation)
	if err != nil {
		return "", fmt.Errorf("failed to get collation for database %s: %w", name, err)
	}
	return collation, nil
}

// --- Login operations ---

func (c *MSSQLClient) LoginExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := c.db.QueryRowContext(ctx,
		"SELECT CASE WHEN SUSER_ID(@p1) IS NOT NULL THEN 1 ELSE 0 END", name).Scan(&exists)
	return exists, err
}

func (c *MSSQLClient) CreateLogin(ctx context.Context, name, password string) error {
	query := fmt.Sprintf("CREATE LOGIN %s WITH PASSWORD = %s", QuoteName(name), QuoteString(password))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create login %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) DropLogin(ctx context.Context, name string) error {
	query := fmt.Sprintf("DROP LOGIN %s", QuoteName(name))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to drop login %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) UpdateLoginPassword(ctx context.Context, name, password string) error {
	query := fmt.Sprintf("ALTER LOGIN %s WITH PASSWORD = %s", QuoteName(name), QuoteString(password))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to update password for login %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) GetLoginDefaultDatabase(ctx context.Context, name string) (string, error) {
	var defaultDB string
	err := c.db.QueryRowContext(ctx,
		"SELECT default_database_name FROM sys.server_principals WHERE name = @p1", name).Scan(&defaultDB)
	if err != nil {
		return "", fmt.Errorf("failed to get default database for login %s: %w", name, err)
	}
	return defaultDB, nil
}

func (c *MSSQLClient) SetLoginDefaultDatabase(ctx context.Context, name, dbName string) error {
	query := fmt.Sprintf("ALTER LOGIN %s WITH DEFAULT_DATABASE = %s", QuoteName(name), QuoteName(dbName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to set default database for login %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) GetLoginServerRoles(ctx context.Context, name string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT r.name FROM sys.server_role_members m
		 JOIN sys.server_principals r ON m.role_principal_id = r.principal_id
		 JOIN sys.server_principals p ON m.member_principal_id = p.principal_id
		 WHERE p.name = @p1`, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get server roles for login %s: %w", name, err)
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (c *MSSQLClient) AddLoginToServerRole(ctx context.Context, login, role string) error {
	query := fmt.Sprintf("ALTER SERVER ROLE %s ADD MEMBER %s", QuoteName(role), QuoteName(login))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to add login %s to server role %s: %w", login, role, err)
	}
	return nil
}

func (c *MSSQLClient) RemoveLoginFromServerRole(ctx context.Context, login, role string) error {
	query := fmt.Sprintf("ALTER SERVER ROLE %s DROP MEMBER %s", QuoteName(role), QuoteName(login))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to remove login %s from server role %s: %w", login, role, err)
	}
	return nil
}

// --- DatabaseUser operations ---

func (c *MSSQLClient) UserExists(ctx context.Context, dbName, userName string) (bool, error) {
	var exists bool
	query := "SELECT CASE WHEN DATABASE_PRINCIPAL_ID(@p1) IS NOT NULL THEN 1 ELSE 0 END"
	err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
		return conn.QueryRowContext(ctx, query, userName).Scan(&exists)
	})
	return exists, err
}

func (c *MSSQLClient) CreateUser(ctx context.Context, dbName, userName, loginName string) error {
	query := fmt.Sprintf("CREATE USER %s FOR LOGIN %s", QuoteName(userName), QuoteName(loginName))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) DropUser(ctx context.Context, dbName, userName string) error {
	query := fmt.Sprintf("DROP USER IF EXISTS %s", QuoteName(userName))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) GetUserDatabaseRoles(ctx context.Context, dbName, userName string) ([]string, error) {
	var roles []string
	err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx,
			`SELECT r.name FROM sys.database_role_members m
			 JOIN sys.database_principals r ON m.role_principal_id = r.principal_id
			 JOIN sys.database_principals u ON m.member_principal_id = u.principal_id
			 WHERE u.name = @p1`, userName)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var role string
			if err := rows.Scan(&role); err != nil {
				return err
			}
			roles = append(roles, role)
		}
		return rows.Err()
	})
	return roles, err
}

func (c *MSSQLClient) AddUserToDatabaseRole(ctx context.Context, dbName, userName, role string) error {
	query := fmt.Sprintf("ALTER ROLE %s ADD MEMBER %s", QuoteName(role), QuoteName(userName))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) RemoveUserFromDatabaseRole(ctx context.Context, dbName, userName, role string) error {
	query := fmt.Sprintf("ALTER ROLE %s DROP MEMBER %s", QuoteName(role), QuoteName(userName))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) UserOwnsObjects(ctx context.Context, dbName, userName string) (bool, error) {
	var count int
	err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
		// Check both object ownership (sys.objects) and schema ownership (sys.schemas).
		// DROP USER will fail if the user owns any schema or object.
		return conn.QueryRowContext(ctx,
			`SELECT (
				SELECT COUNT(*) FROM sys.objects WHERE principal_id = DATABASE_PRINCIPAL_ID(@p1)
			) + (
				SELECT COUNT(*) FROM sys.schemas
				WHERE principal_id = DATABASE_PRINCIPAL_ID(@p1)
				AND name NOT IN ('dbo', 'guest', 'INFORMATION_SCHEMA', 'sys')
			)`,
			userName).Scan(&count)
	})
	return count > 0, err
}

// --- Cross-reference checks ---

func (c *MSSQLClient) LoginHasUsers(ctx context.Context, loginName string) (bool, error) {
	// Check all user databases for users mapped to this login.
	// We iterate over non-system databases and look for mapped users.
	rows, err := c.db.QueryContext(ctx,
		"SELECT name FROM sys.databases WHERE database_id > 4 AND state_desc = 'ONLINE'")
	if err != nil {
		return false, fmt.Errorf("failed to list databases: %w", err)
	}
	defer rows.Close()

	var dbNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		dbNames = append(dbNames, name)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	for _, dbName := range dbNames {
		var count int
		err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
			return conn.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM sys.database_principals
				 WHERE type = 'S' AND sid = SUSER_SID(@p1)`, loginName).Scan(&count)
		})
		if err != nil {
			continue // skip databases we can't access
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

// --- Schema operations ---

func (c *MSSQLClient) SchemaExists(ctx context.Context, dbName, schemaName string) (bool, error) {
	var exists bool
	err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
		return conn.QueryRowContext(ctx,
			"SELECT CASE WHEN SCHEMA_ID(@p1) IS NOT NULL THEN 1 ELSE 0 END", schemaName).Scan(&exists)
	})
	return exists, err
}

func (c *MSSQLClient) CreateSchema(ctx context.Context, dbName, schemaName string, owner *string) error {
	query := fmt.Sprintf("CREATE SCHEMA %s", QuoteName(schemaName))
	if owner != nil && *owner != "" {
		query += fmt.Sprintf(" AUTHORIZATION %s", QuoteName(*owner))
	}
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) DropSchema(ctx context.Context, dbName, schemaName string) error {
	query := fmt.Sprintf("DROP SCHEMA %s", QuoteName(schemaName))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) GetSchemaOwner(ctx context.Context, dbName, schemaName string) (string, error) {
	var owner string
	err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
		return conn.QueryRowContext(ctx,
			`SELECT dp.name FROM sys.schemas s
			 JOIN sys.database_principals dp ON s.principal_id = dp.principal_id
			 WHERE s.name = @p1`, schemaName).Scan(&owner)
	})
	if err != nil {
		return "", fmt.Errorf("failed to get owner for schema %s: %w", schemaName, err)
	}
	return owner, nil
}

func (c *MSSQLClient) SetSchemaOwner(ctx context.Context, dbName, schemaName, owner string) error {
	query := fmt.Sprintf("ALTER AUTHORIZATION ON SCHEMA::%s TO %s", QuoteName(schemaName), QuoteName(owner))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) SchemaHasObjects(ctx context.Context, dbName, schemaName string) (bool, error) {
	var count int
	err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
		return conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sys.objects WHERE schema_id = SCHEMA_ID(@p1)", schemaName).Scan(&count)
	})
	if err != nil {
		return false, fmt.Errorf("failed to check objects in schema %s: %w", schemaName, err)
	}
	return count > 0, nil
}

// --- Permission operations ---

func (c *MSSQLClient) GetPermissions(ctx context.Context, dbName, userName string) ([]PermissionState, error) {
	var perms []PermissionState
	err := c.queryInDatabase(ctx, dbName, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx,
			`SELECT dp.permission_name, dp.class_desc,
			        ISNULL(SCHEMA_NAME(dp.major_id), OBJECT_NAME(dp.major_id)),
			        dp.state_desc
			 FROM sys.database_permissions dp
			 JOIN sys.database_principals pr ON dp.grantee_principal_id = pr.principal_id
			 WHERE pr.name = @p1 AND dp.state_desc IN ('GRANT', 'DENY')`, userName)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var perm, classDesc, targetName, state string
			if err := rows.Scan(&perm, &classDesc, &targetName, &state); err != nil {
				return err
			}
			target := formatTarget(classDesc, targetName)
			perms = append(perms, PermissionState{
				Permission: perm,
				Target:     target,
				State:      state,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get permissions for user %s: %w", userName, err)
	}
	return perms, nil
}

func formatTarget(classDesc, targetName string) string {
	switch classDesc {
	case "SCHEMA":
		return "SCHEMA::" + targetName
	case "OBJECT_OR_COLUMN":
		return "OBJECT::" + targetName
	case "DATABASE":
		return "DATABASE"
	default:
		return classDesc + "::" + targetName
	}
}

func (c *MSSQLClient) GrantPermission(ctx context.Context, dbName, permission, target, userName string) error {
	if !IsValidPermission(permission) {
		return fmt.Errorf("invalid permission: %s", permission)
	}
	query := fmt.Sprintf("GRANT %s ON %s TO %s", strings.ToUpper(permission), QuotePermissionTarget(target), QuoteName(userName))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) DenyPermission(ctx context.Context, dbName, permission, target, userName string) error {
	if !IsValidPermission(permission) {
		return fmt.Errorf("invalid permission: %s", permission)
	}
	query := fmt.Sprintf("DENY %s ON %s TO %s", strings.ToUpper(permission), QuotePermissionTarget(target), QuoteName(userName))
	return c.execInDatabase(ctx, dbName, query)
}

func (c *MSSQLClient) RevokePermission(ctx context.Context, dbName, permission, target, userName string) error {
	if !IsValidPermission(permission) {
		return fmt.Errorf("invalid permission: %s", permission)
	}
	query := fmt.Sprintf("REVOKE %s ON %s FROM %s", strings.ToUpper(permission), QuotePermissionTarget(target), QuoteName(userName))
	return c.execInDatabase(ctx, dbName, query)
}

// --- Helpers ---

// queryInDatabase executes fn on a dedicated connection after switching to the given database.
// This ensures USE + query happen on the same connection from the pool.
func (c *MSSQLClient) queryInDatabase(ctx context.Context, dbName string, fn func(conn *sql.Conn) error) error {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer conn.Close()

	_, err = conn.ExecContext(ctx, fmt.Sprintf("USE %s", QuoteName(dbName)))
	if err != nil {
		return fmt.Errorf("failed to switch to database %s: %w", dbName, err)
	}

	return fn(conn)
}

// execInDatabase executes a statement in the context of a specific database.
func (c *MSSQLClient) execInDatabase(ctx context.Context, dbName, query string) error {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer conn.Close()

	_, err = conn.ExecContext(ctx, fmt.Sprintf("USE %s", QuoteName(dbName)))
	if err != nil {
		return fmt.Errorf("failed to switch to database %s: %w", dbName, err)
	}

	_, err = conn.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to execute in database %s: %w", dbName, err)
	}
	return nil
}
