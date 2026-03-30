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
			 WHERE pr.name = @p1 AND dp.state_desc IN ('GRANT', 'DENY')
			 AND dp.permission_name != 'CONNECT'`, userName)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var perm, classDesc, state string
			var targetName sql.NullString
			if err := rows.Scan(&perm, &classDesc, &targetName, &state); err != nil {
				return err
			}
			target := formatTarget(classDesc, targetName.String)
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

// --- Availability Group operations ---

func (c *MSSQLClient) AGExists(ctx context.Context, agName string) (bool, error) {
	var exists bool
	err := c.db.QueryRowContext(ctx,
		"SELECT CASE WHEN EXISTS (SELECT 1 FROM sys.availability_groups WHERE name = @p1) THEN 1 ELSE 0 END",
		agName).Scan(&exists)
	return exists, err
}

func (c *MSSQLClient) CreateAG(ctx context.Context, config *AGConfig) error {
	// Build CREATE AVAILABILITY GROUP statement.
	// T-SQL syntax: CREATE AVAILABILITY GROUP [name] WITH (...) FOR REPLICA ON ...
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE AVAILABILITY GROUP %s\n", QuoteName(config.Name))

	clusterType := config.ClusterType
	if clusterType == "" {
		clusterType = "EXTERNAL"
	}
	fmt.Fprintf(&b, "WITH (\n    CLUSTER_TYPE = %s,\n", clusterType)
	fmt.Fprintf(&b, "    AUTOMATED_BACKUP_PREFERENCE = %s,\n", config.AutomatedBackupPreference)
	if config.DBFailover {
		b.WriteString("    DB_FAILOVER = ON\n")
	} else {
		b.WriteString("    DB_FAILOVER = OFF\n")
	}
	b.WriteString(")\n")

	// FOR REPLICA ON clause (required, must come before FOR DATABASE)
	b.WriteString("FOR REPLICA ON\n")
	for i, replica := range config.Replicas {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "    N'%s' WITH (\n", strings.ReplaceAll(replica.ServerName, "'", "''"))
		fmt.Fprintf(&b, "        ENDPOINT_URL = N'%s',\n", strings.ReplaceAll(replica.EndpointURL, "'", "''"))
		fmt.Fprintf(&b, "        AVAILABILITY_MODE = %s,\n", replica.AvailabilityMode)
		fmt.Fprintf(&b, "        FAILOVER_MODE = %s,\n", replica.FailoverMode)
		fmt.Fprintf(&b, "        SEEDING_MODE = %s,\n", replica.SeedingMode)
		fmt.Fprintf(&b, "        SECONDARY_ROLE (ALLOW_CONNECTIONS = %s)\n", replica.SecondaryRole)
		b.WriteString("    )")
	}
	b.WriteString(";")

	if _, err := c.db.ExecContext(ctx, b.String()); err != nil {
		return fmt.Errorf("failed to create availability group %s: %w", config.Name, err)
	}

	// Add databases to AG after creation (separate ALTER statements)
	for _, db := range config.Databases {
		if err := c.AddDatabaseToAG(ctx, config.Name, db); err != nil {
			return fmt.Errorf("failed to add database %s to AG: %w", db, err)
		}
	}
	return nil
}

func (c *MSSQLClient) GetAGStatus(ctx context.Context, agName string) (*AGStatus, error) {
	status := &AGStatus{Name: agName}

	// Get primary replica
	err := c.db.QueryRowContext(ctx,
		`SELECT ags.primary_replica
		 FROM sys.dm_hadr_availability_group_states ags
		 JOIN sys.availability_groups ag ON ags.group_id = ag.group_id
		 WHERE ag.name = @p1`, agName).Scan(&status.PrimaryReplica)
	if err != nil {
		return nil, fmt.Errorf("failed to get AG state for %s: %w", agName, err)
	}

	// Get replica states
	rows, err := c.db.QueryContext(ctx,
		`SELECT ar.replica_server_name,
		        ISNULL(ars.role_desc, 'RESOLVING'),
		        ISNULL(ars.synchronization_health_desc, 'NOT_HEALTHY'),
		        ISNULL(ars.connected_state_desc, 'DISCONNECTED')
		 FROM sys.availability_replicas ar
		 JOIN sys.availability_groups ag ON ar.group_id = ag.group_id
		 LEFT JOIN sys.dm_hadr_availability_replica_states ars ON ar.replica_id = ars.replica_id
		 WHERE ag.name = @p1`, agName)
	if err != nil {
		return nil, fmt.Errorf("failed to get replica states for AG %s: %w", agName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var rs AGReplicaState
		var connState string
		if err := rows.Scan(&rs.ServerName, &rs.Role, &rs.SynchronizationState, &connState); err != nil {
			return nil, err
		}
		rs.Connected = connState == "CONNECTED"
		status.Replicas = append(status.Replicas, rs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Get database states
	dbRows, err := c.db.QueryContext(ctx,
		`SELECT d.name,
		        ISNULL(drs.synchronization_state_desc, 'NOT_SYNCHRONIZING'),
		        CASE WHEN drs.is_local = 1 THEN 1 ELSE 0 END
		 FROM sys.availability_databases_cluster adc
		 JOIN sys.availability_groups ag ON adc.group_id = ag.group_id
		 JOIN sys.databases d ON adc.database_name = d.name
		 LEFT JOIN sys.dm_hadr_database_replica_states drs ON adc.group_database_id = drs.group_database_id AND drs.is_local = 1
		 WHERE ag.name = @p1`, agName)
	if err != nil {
		return nil, fmt.Errorf("failed to get database states for AG %s: %w", agName, err)
	}
	defer dbRows.Close()

	for dbRows.Next() {
		var ds AGDatabaseState
		var isLocal int
		if err := dbRows.Scan(&ds.Name, &ds.SynchronizationState, &isLocal); err != nil {
			return nil, err
		}
		ds.Joined = isLocal == 1
		status.Databases = append(status.Databases, ds)
	}

	return status, dbRows.Err()
}

func (c *MSSQLClient) AddDatabaseToAG(ctx context.Context, agName, dbName string) error {
	query := fmt.Sprintf("ALTER AVAILABILITY GROUP %s ADD DATABASE %s", QuoteName(agName), QuoteName(dbName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to add database %s to AG %s: %w", dbName, agName, err)
	}
	return nil
}

func (c *MSSQLClient) RemoveDatabaseFromAG(ctx context.Context, agName, dbName string) error {
	query := fmt.Sprintf("ALTER AVAILABILITY GROUP %s REMOVE DATABASE %s", QuoteName(agName), QuoteName(dbName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to remove database %s from AG %s: %w", dbName, agName, err)
	}
	return nil
}

func (c *MSSQLClient) JoinAG(ctx context.Context, agName, clusterType string) error {
	query := fmt.Sprintf("ALTER AVAILABILITY GROUP %s JOIN", QuoteName(agName))
	if clusterType == "NONE" {
		query += " WITH (CLUSTER_TYPE = NONE)"
	}
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to join AG %s: %w", agName, err)
	}
	return nil
}

func (c *MSSQLClient) GrantAGCreateDatabase(ctx context.Context, agName string) error {
	query := fmt.Sprintf("ALTER AVAILABILITY GROUP %s GRANT CREATE ANY DATABASE", QuoteName(agName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to grant create database on AG %s: %w", agName, err)
	}
	return nil
}

func (c *MSSQLClient) AddListenerToAG(ctx context.Context, agName string, listener AGListenerConfig) error {
	query := fmt.Sprintf("ALTER AVAILABILITY GROUP %s\nADD LISTENER N'%s' (\n",
		QuoteName(agName), strings.ReplaceAll(listener.Name, "'", "''"))

	if len(listener.IPAddresses) > 0 {
		query += "    WITH IP (\n"
		for i, ip := range listener.IPAddresses {
			if i > 0 {
				query += ",\n"
			}
			query += fmt.Sprintf("        (N'%s', N'%s')", ip.IP, ip.SubnetMask)
		}
		query += "\n    ),\n"
	}
	query += fmt.Sprintf("    PORT = %d\n);", listener.Port)

	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to add listener to AG %s: %w", agName, err)
	}
	return nil
}

func (c *MSSQLClient) FailoverAG(ctx context.Context, agName string) error {
	query := fmt.Sprintf("ALTER AVAILABILITY GROUP %s FAILOVER", QuoteName(agName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to failover AG %s: %w", agName, err)
	}
	return nil
}

func (c *MSSQLClient) ForceFailoverAG(ctx context.Context, agName string) error {
	query := fmt.Sprintf("ALTER AVAILABILITY GROUP %s FORCE_FAILOVER_ALLOW_DATA_LOSS", QuoteName(agName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to force failover AG %s: %w", agName, err)
	}
	return nil
}

func (c *MSSQLClient) GetAGReplicaRole(ctx context.Context, agName, serverName string) (string, error) {
	var role string
	err := c.db.QueryRowContext(ctx,
		`SELECT ISNULL(ars.role_desc, 'RESOLVING')
		 FROM sys.availability_replicas ar
		 JOIN sys.availability_groups ag ON ar.group_id = ag.group_id
		 LEFT JOIN sys.dm_hadr_availability_replica_states ars ON ar.replica_id = ars.replica_id
		 WHERE ag.name = @p1 AND ar.replica_server_name = @p2`, agName, serverName).Scan(&role)
	if err != nil {
		return "", fmt.Errorf("failed to get role for replica %s in AG %s: %w", serverName, agName, err)
	}
	return role, nil
}

func (c *MSSQLClient) DropAG(ctx context.Context, agName string) error {
	query := fmt.Sprintf("DROP AVAILABILITY GROUP %s", QuoteName(agName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to drop availability group %s: %w", agName, err)
	}
	return nil
}

func (c *MSSQLClient) CreateHADREndpoint(ctx context.Context, port int) error {
	query := fmt.Sprintf(`CREATE ENDPOINT hadr_endpoint
    STATE = STARTED
    AS TCP (LISTENER_PORT = %d)
    FOR DATABASE_MIRRORING (ROLE = ALL)`, port)
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create HADR endpoint on port %d: %w", port, err)
	}
	return nil
}

func (c *MSSQLClient) HADREndpointExists(ctx context.Context) (bool, error) {
	var exists bool
	err := c.db.QueryRowContext(ctx,
		"SELECT CASE WHEN EXISTS (SELECT 1 FROM sys.database_mirroring_endpoints) THEN 1 ELSE 0 END").Scan(&exists)
	return exists, err
}

// --- Certificate operations for HADR ---

func (c *MSSQLClient) CreateMasterKey(ctx context.Context, password string) error {
	query := fmt.Sprintf("CREATE MASTER KEY ENCRYPTION BY PASSWORD = %s", QuoteString(password))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create master key: %w", err)
	}
	return nil
}

func (c *MSSQLClient) MasterKeyExists(ctx context.Context) (bool, error) {
	var exists bool
	err := c.db.QueryRowContext(ctx,
		"SELECT CASE WHEN EXISTS (SELECT 1 FROM sys.symmetric_keys WHERE name = '##MS_DatabaseMasterKey##') THEN 1 ELSE 0 END").Scan(&exists)
	return exists, err
}

func (c *MSSQLClient) CreateCertificate(ctx context.Context, certName, subject, expiryDate string) error {
	query := fmt.Sprintf("CREATE CERTIFICATE %s WITH SUBJECT = %s, EXPIRY_DATE = %s",
		QuoteName(certName), QuoteString(subject), QuoteString(expiryDate))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create certificate %s: %w", certName, err)
	}
	return nil
}

func (c *MSSQLClient) CertificateExists(ctx context.Context, certName string) (bool, error) {
	var exists bool
	err := c.db.QueryRowContext(ctx,
		"SELECT CASE WHEN EXISTS (SELECT 1 FROM sys.certificates WHERE name = @p1) THEN 1 ELSE 0 END", certName).Scan(&exists)
	return exists, err
}

func (c *MSSQLClient) BackupCertificate(ctx context.Context, certName, certPath, keyPath, encryptionPassword string) error {
	query := fmt.Sprintf("BACKUP CERTIFICATE %s TO FILE = %s WITH PRIVATE KEY (FILE = %s, ENCRYPTION BY PASSWORD = %s)",
		QuoteName(certName), QuoteString(certPath), QuoteString(keyPath), QuoteString(encryptionPassword))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to backup certificate %s: %w", certName, err)
	}
	return nil
}

func (c *MSSQLClient) CreateCertificateFromBackup(ctx context.Context, certName, certPath, keyPath, decryptionPassword string) error {
	query := fmt.Sprintf("CREATE CERTIFICATE %s FROM FILE = %s WITH PRIVATE KEY (FILE = %s, DECRYPTION BY PASSWORD = %s)",
		QuoteName(certName), QuoteString(certPath), QuoteString(keyPath), QuoteString(decryptionPassword))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create certificate %s from backup: %w", certName, err)
	}
	return nil
}

func (c *MSSQLClient) CreateLoginFromCertificate(ctx context.Context, loginName, certName string) error {
	query := fmt.Sprintf("CREATE LOGIN %s FROM CERTIFICATE %s", QuoteName(loginName), QuoteName(certName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create login %s from certificate %s: %w", loginName, certName, err)
	}
	return nil
}

func (c *MSSQLClient) GrantEndpointConnect(ctx context.Context, endpointName, loginName string) error {
	query := fmt.Sprintf("GRANT CONNECT ON ENDPOINT::%s TO %s", QuoteName(endpointName), QuoteName(loginName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to grant connect on endpoint %s to %s: %w", endpointName, loginName, err)
	}
	return nil
}

func (c *MSSQLClient) CreateHADREndpointWithCert(ctx context.Context, port int, certName string) error {
	query := fmt.Sprintf(`CREATE ENDPOINT hadr_endpoint
    STATE = STARTED
    AS TCP (LISTENER_PORT = %d)
    FOR DATABASE_MIRRORING (
        ROLE = ALL,
        AUTHENTICATION = CERTIFICATE %s,
        ENCRYPTION = DISABLED
    )`, port, QuoteName(certName))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to create HADR endpoint with cert on port %d: %w", port, err)
	}
	return nil
}

// --- Backup/Restore operations ---

func (c *MSSQLClient) BackupDatabase(ctx context.Context, dbName, destination, backupType string, compression bool) error {
	query := fmt.Sprintf("BACKUP DATABASE %s TO DISK = %s", QuoteName(dbName), QuoteString(destination))

	var withClauses []string
	switch backupType {
	case "Differential":
		withClauses = append(withClauses, "DIFFERENTIAL")
	case "Log":
		// Log backup uses BACKUP LOG, not BACKUP DATABASE
		query = fmt.Sprintf("BACKUP LOG %s TO DISK = %s", QuoteName(dbName), QuoteString(destination))
	}
	if compression {
		withClauses = append(withClauses, "COMPRESSION")
	}
	if len(withClauses) > 0 {
		query += " WITH " + strings.Join(withClauses, ", ")
	}

	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to backup database %s: %w", dbName, err)
	}
	return nil
}

func (c *MSSQLClient) RestoreDatabase(ctx context.Context, dbName, source string) error {
	query := fmt.Sprintf("RESTORE DATABASE %s FROM DISK = %s WITH REPLACE", QuoteName(dbName), QuoteString(source))
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to restore database %s: %w", dbName, err)
	}
	return nil
}

// --- Server info ---

func (c *MSSQLClient) GetServerVersion(ctx context.Context) (string, error) {
	var version string
	err := c.db.QueryRowContext(ctx, "SELECT SERVERPROPERTY('ProductVersion')").Scan(&version)
	if err != nil {
		return "", fmt.Errorf("failed to get server version: %w", err)
	}
	return version, nil
}

func (c *MSSQLClient) GetServerEdition(ctx context.Context) (string, error) {
	var edition string
	err := c.db.QueryRowContext(ctx, "SELECT SERVERPROPERTY('Edition')").Scan(&edition)
	if err != nil {
		return "", fmt.Errorf("failed to get server edition: %w", err)
	}
	return edition, nil
}

// --- Database configuration ---

func (c *MSSQLClient) GetDatabaseRecoveryModel(ctx context.Context, name string) (string, error) {
	var model string
	query := fmt.Sprintf("SELECT recovery_model_desc FROM sys.databases WHERE name = %s", QuoteString(name))
	err := c.db.QueryRowContext(ctx, query).Scan(&model)
	if err != nil {
		return "", fmt.Errorf("failed to get recovery model for %s: %w", name, err)
	}
	return model, nil
}

func (c *MSSQLClient) SetDatabaseRecoveryModel(ctx context.Context, name, model string) error {
	query := fmt.Sprintf("ALTER DATABASE %s SET RECOVERY %s", QuoteName(name), model)
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to set recovery model for %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) GetDatabaseCompatibilityLevel(ctx context.Context, name string) (int, error) {
	var level int
	query := fmt.Sprintf("SELECT compatibility_level FROM sys.databases WHERE name = %s", QuoteString(name))
	err := c.db.QueryRowContext(ctx, query).Scan(&level)
	if err != nil {
		return 0, fmt.Errorf("failed to get compatibility level for %s: %w", name, err)
	}
	return level, nil
}

func (c *MSSQLClient) SetDatabaseCompatibilityLevel(ctx context.Context, name string, level int) error {
	query := fmt.Sprintf("ALTER DATABASE %s SET COMPATIBILITY_LEVEL = %d", QuoteName(name), level)
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to set compatibility level for %s: %w", name, err)
	}
	return nil
}

func (c *MSSQLClient) GetDatabaseOption(ctx context.Context, name, option string) (bool, error) {
	// Use CAST to int to normalize the sql_variant return type of DATABASEPROPERTYEX.
	query := fmt.Sprintf("SELECT CAST(DATABASEPROPERTYEX(%s, %s) AS int)", QuoteString(name), QuoteString(option))
	var val sql.NullInt64
	err := c.db.QueryRowContext(ctx, query).Scan(&val)
	if err != nil {
		return false, fmt.Errorf("failed to get database option %s for %s: %w", option, name, err)
	}
	return val.Valid && val.Int64 == 1, nil
}

func (c *MSSQLClient) SetDatabaseOption(ctx context.Context, name, option string, value bool) error {
	valStr := "OFF"
	if value {
		valStr = "ON"
	}
	// Some options (e.g., READ_COMMITTED_SNAPSHOT) require exclusive access.
	// WITH ROLLBACK IMMEDIATE terminates existing transactions to allow the change.
	query := fmt.Sprintf("ALTER DATABASE %s SET %s %s WITH ROLLBACK IMMEDIATE", QuoteName(name), option, valStr)
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to set database option %s for %s: %w", option, name, err)
	}
	return nil
}

// --- Point-in-Time Restore ---

func (c *MSSQLClient) RestoreDatabasePIT(ctx context.Context, dbName, fullSource, logSource, stopAt string) error {
	// Step 1: Restore full backup with NORECOVERY (prepares the database for log restore)
	query1 := fmt.Sprintf("RESTORE DATABASE %s FROM DISK = %s WITH REPLACE, NORECOVERY",
		QuoteName(dbName), QuoteString(fullSource))
	if _, err := c.db.ExecContext(ctx, query1); err != nil {
		return fmt.Errorf("failed to restore full backup for %s: %w", dbName, err)
	}

	// Step 2: Restore log backup with STOPAT and RECOVERY
	query2 := fmt.Sprintf("RESTORE LOG %s FROM DISK = %s WITH RECOVERY, STOPAT = %s",
		QuoteName(dbName), QuoteString(logSource), QuoteString(stopAt))
	if _, err := c.db.ExecContext(ctx, query2); err != nil {
		return fmt.Errorf("failed to restore log to point-in-time %s for %s: %w", stopAt, dbName, err)
	}
	return nil
}

func (c *MSSQLClient) RestoreDatabaseWithMove(ctx context.Context, dbName, source string, withMove map[string]string) error {
	parts := []string{fmt.Sprintf("RESTORE DATABASE %s FROM DISK = %s WITH REPLACE", QuoteName(dbName), QuoteString(source))}
	for logicalName, physicalPath := range withMove {
		parts = append(parts, fmt.Sprintf("MOVE %s TO %s", QuoteString(logicalName), QuoteString(physicalPath)))
	}
	query := strings.Join(parts, ", ")
	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to restore database %s with move: %w", dbName, err)
	}
	return nil
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
