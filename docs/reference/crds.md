# CRD Reference

All CRDs are in API group `mssql.popul.io/v1alpha1`.

## Common types

### ServerReference

Used by all CRDs to connect to SQL Server.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | `string` | yes | | Hostname or IP of the SQL Server instance |
| `port` | `*int32` | no | `1433` | TCP port |
| `tls` | `*bool` | no | `false` | Enable TLS encryption |
| `credentialsSecret.name` | `string` | yes | | Name of a Secret with `username` and `password` keys |

### SecretReference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | yes | Name of a Kubernetes Secret (same namespace as the CR) |

### DeletionPolicy

Enum: `Delete` | `Retain`

- `Retain` (default): the SQL Server object is kept when the CR is deleted
- `Delete`: the SQL Server object is dropped when the CR is deleted

---

## Database

**Short name:** `msdb` | **Category:** `mssql`

### Spec

| Field | Type | Required | Default | Immutable | Description |
|-------|------|----------|---------|-----------|-------------|
| `server` | `ServerReference` | yes | | host, port, tls | SQL Server connection |
| `databaseName` | `string` | yes | | yes | Database name on SQL Server |
| `collation` | `*string` | no | SQL Server default | yes | Database collation |
| `owner` | `*string` | no | | no | Database owner (`ALTER AUTHORIZATION`) |
| `deletionPolicy` | `*DeletionPolicy` | no | `Retain` | no | Behavior on CR deletion |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |

### Print columns

```
NAME       DATABASE   READY   AGE
```

---

## Login

**Short name:** `mslogin` | **Category:** `mssql`

### Spec

| Field | Type | Required | Default | Immutable | Description |
|-------|------|----------|---------|-----------|-------------|
| `server` | `ServerReference` | yes | | host, port, tls | SQL Server connection |
| `loginName` | `string` | yes | | yes | SQL Server login name |
| `passwordSecret` | `SecretReference` | yes | | no | Secret with `password` key |
| `defaultDatabase` | `*string` | no | | no | Default database for the login |
| `serverRoles` | `[]string` | no | | no | Server-level roles |
| `deletionPolicy` | `*DeletionPolicy` | no | `Retain` | no | Behavior on CR deletion |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |
| `passwordSecretResourceVersion` | `string` | Tracks Secret version for password rotation detection |

### Print columns

```
NAME           LOGIN        READY   AGE
```

---

## DatabaseUser

**Short name:** `msuser` | **Category:** `mssql`

### Spec

| Field | Type | Required | Default | Immutable | Description |
|-------|------|----------|---------|-----------|-------------|
| `server` | `ServerReference` | yes | | host, port, tls | SQL Server connection |
| `databaseName` | `string` | yes | | yes | Target database |
| `userName` | `string` | yes | | yes | User name inside the database |
| `loginRef.name` | `string` | yes | | yes | Name of a Login CR (same namespace) |
| `databaseRoles` | `[]string` | no | | no | Database-level roles |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |

### Print columns

```
NAME           DATABASE   USER         READY   AGE
```

### Deletion behavior

The user is always dropped on CR deletion (no `deletionPolicy`). Deletion is blocked if the user owns objects in the database.

---

## Schema

**Short name:** `msschema` | **Category:** `mssql`

### Spec

| Field | Type | Required | Default | Immutable | Description |
|-------|------|----------|---------|-----------|-------------|
| `server` | `ServerReference` | yes | | host, port, tls | SQL Server connection |
| `databaseName` | `string` | yes | | yes | Target database |
| `schemaName` | `string` | yes | | yes | Schema name inside the database |
| `owner` | `*string` | no | `dbo` | no | Schema owner (`ALTER AUTHORIZATION ON SCHEMA`) |
| `deletionPolicy` | `*DeletionPolicy` | no | `Retain` | no | Behavior on CR deletion |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |

### Print columns

```
NAME          DATABASE   SCHEMA   READY   AGE
```

### Deletion behavior

With `Delete` policy, deletion is blocked if the schema contains objects. The operator requeues until the objects are moved or dropped.

---

## Permission

**Short name:** `msperm` | **Category:** `mssql`

### Spec

| Field | Type | Required | Default | Immutable | Description |
|-------|------|----------|---------|-----------|-------------|
| `server` | `ServerReference` | yes | | host, port, tls | SQL Server connection |
| `databaseName` | `string` | yes | | yes | Target database |
| `userName` | `string` | yes | | yes | Database user to manage permissions for |
| `grants` | `[]PermissionEntry` | no | | no | Permissions to GRANT |
| `denies` | `[]PermissionEntry` | no | | no | Permissions to DENY |

### PermissionEntry

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `permission` | `string` | yes | SQL Server permission keyword (e.g. `SELECT`, `INSERT`) |
| `on` | `string` | yes | Target scope (e.g. `SCHEMA::app`, `OBJECT::dbo.Users`) |

**Supported permissions:** `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `EXECUTE`, `ALTER`, `CONTROL`, `REFERENCES`, `VIEW DEFINITION`, `CREATE TABLE`, `CREATE VIEW`, `CREATE PROCEDURE`, `CREATE FUNCTION`, `CREATE SCHEMA`.

**Supported target formats:**

| Format | Example |
|--------|---------|
| `SCHEMA::name` | `SCHEMA::app` |
| `OBJECT::schema.name` | `OBJECT::dbo.Users` |
| `DATABASE::name` | `DATABASE::myapp` |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |

### Print columns

```
NAME              DATABASE   USER        READY   AGE
```

### Deletion behavior

All grants and denies are REVOKEd on CR deletion. There is no `deletionPolicy`.

---

## Backup

Short name: `msbak` | Category: `mssql`

One-shot database backup. Executes once, then remains in `Completed` or `Failed` phase.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `server` | `ServerReference` | yes | | SQL Server connection (see Common types) |
| `databaseName` | `string` | yes | | Database to back up |
| `destination` | `string` | yes | | File path on the SQL Server filesystem (e.g. `/var/opt/mssql/backups/mydb.bak`) |
| `type` | `BackupType` | no | `Full` | `Full`, `Differential`, or `Log` |
| `compression` | `*bool` | no | `false` | Enable backup compression |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `BackupPhase` | `Pending`, `Running`, `Completed`, `Failed` |
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |
| `startTime` | `*metav1.Time` | When the backup started |
| `completionTime` | `*metav1.Time` | When the backup completed |
| `backupSize` | `*int64` | Backup file size in bytes (if reported by SQL Server) |

### Print columns

```
NAME              DATABASE   TYPE   PHASE       AGE
```

### Immutability

Spec is fully immutable after creation. To re-run a backup, delete the CR and create a new one.

---

## Restore

Short name: `msrestore` | Category: `mssql`

One-shot database restore. Executes once, then remains in `Completed` or `Failed` phase.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `server` | `ServerReference` | yes | | SQL Server connection (see Common types) |
| `databaseName` | `string` | yes | | Target database name for the restore |
| `source` | `string` | yes | | Backup file path on the SQL Server filesystem |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `RestorePhase` | `Pending`, `Running`, `Completed`, `Failed` |
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |
| `startTime` | `*metav1.Time` | When the restore started |
| `completionTime` | `*metav1.Time` | When the restore completed |

### Print columns

```
NAME              DATABASE   PHASE       AGE
```

### Immutability

Spec is fully immutable after creation. To re-run a restore, delete the CR and create a new one.

---

## AvailabilityGroup

Short name: `msag` | Category: `mssql`

Manages an Always On Availability Group for SQL Server high availability.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `agName` | `string` | yes | | Name of the AG on SQL Server (immutable) |
| `replicas` | `[]AGReplicaSpec` | yes (min 2) | | Replicas participating in the AG |
| `databases` | `[]AGDatabaseSpec` | no | `[]` | Databases to include in the AG |
| `listener` | `*AGListenerSpec` | no | | AG listener configuration |
| `automatedBackupPreference` | `*string` | no | `Secondary` | Where automated backups run: `Primary`, `Secondary`, `SecondaryOnly`, `None` |
| `dbFailover` | `*bool` | no | `true` | Enable database-level health detection for automatic failover |

### AGReplicaSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `serverName` | `string` | yes | | SQL Server instance name |
| `endpointURL` | `string` | yes | | Database mirroring endpoint (e.g. `TCP://sql-0:5022`) |
| `availabilityMode` | `AvailabilityMode` | no | `SynchronousCommit` | `SynchronousCommit` or `AsynchronousCommit` |
| `failoverMode` | `FailoverMode` | no | `Automatic` | `Automatic` (requires sync commit) or `Manual` |
| `seedingMode` | `SeedingMode` | no | `Automatic` | `Automatic` or `Manual` |
| `secondaryRole` | `SecondaryRole` | no | `No` | `No`, `AllowReadIntentOnly`, `AllowAllConnections` |
| `server` | `ServerReference` | yes | | Connection details for this replica |

### AGListenerSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | yes | | Listener DNS name |
| `port` | `*int32` | no | `1433` | Listener port |
| `ipAddresses` | `[]AGListenerIP` | no | `[]` | Static IP addresses with subnet masks |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |
| `primaryReplica` | `string` | Current primary server name |
| `replicas` | `[]AGReplicaStatus` | Observed state of each replica (role, sync state, connected) |
| `databases` | `[]AGDatabaseStatus` | Observed state of each database (sync state, joined) |

### Print columns

```
NAME       AG     PRIMARY   READY   AGE
```

### Immutability

Only `agName` is immutable. Replicas, databases, and listener can be updated after creation.

### Deletion behavior

Dropping the CR drops the AG on SQL Server. Databases are **not** deleted — they remain as standalone databases.
