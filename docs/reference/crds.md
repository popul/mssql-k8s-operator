# CRD Reference

All CRDs are in API group `mssql.popul.io/v1alpha1`.

## Common types

### ServerReference

Used by Database, Login, DatabaseUser, Schema, Permission, Backup, Restore, ScheduledBackup CRDs to connect to SQL Server.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `sqlServerRef` | `*string` | no | | Name of a `SQLServer` CR in the same namespace. When set, host/port/credentials are resolved automatically. |
| `host` | `string` | conditional | | Hostname or IP. Required when `sqlServerRef` is not set. |
| `port` | `*int32` | no | `1433` | TCP port |
| `tls` | `*bool` | no | `false` | Enable TLS encryption |
| `credentialsSecret.name` | `string` | conditional | | Name of a Secret with `username` and `password` keys. Required when `sqlServerRef` is not set. |

> When `sqlServerRef` is set, the controller resolves connection details from the referenced `SQLServer` CR's spec and status. You do not need to specify `host`, `port`, or `credentialsSecret`.

### SecretReference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | yes | Name of a Kubernetes Secret (same namespace as the CR) |

### CrossNamespaceSecretReference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | yes | Name of a Kubernetes Secret |
| `namespace` | `*string` | no | Namespace of the Secret. Defaults to the CR's namespace. |

### DeletionPolicy

Enum: `Delete` | `Retain`

- `Retain` (default): the SQL Server object is kept when the CR is deleted
- `Delete`: the SQL Server object is dropped when the CR is deleted

---

## SQLServer

**Short name:** `mssrv` | **Category:** `mssql`

Defines a SQL Server connection. In **managed mode** (`spec.instance` is set), the operator deploys a StatefulSet, Services, and optionally certificates and an Availability Group. In **external mode** (no `spec.instance`), the operator connects to an existing SQL Server.

### Spec

| Field | Type | Required | Default | Immutable | Description |
|-------|------|----------|---------|-----------|-------------|
| `host` | `string` | conditional | | no | Hostname or IP. Required in external mode. |
| `port` | `*int32` | no | `1433` | no | TCP port |
| `authMethod` | `AuthenticationMethod` | no | `SqlLogin` | no | `SqlLogin`, `AzureAD`, `ManagedIdentity` |
| `credentialsSecret` | `*CrossNamespaceSecretReference` | conditional | | no | Secret with `username`/`password`. Required for SqlLogin. |
| `azureAD` | `*AzureADAuth` | conditional | | no | Azure AD config. Required for AzureAD auth. |
| `managedIdentity` | `*ManagedIdentityAuth` | conditional | | no | Managed Identity config. |
| `tls` | `*bool` | no | `false` | no | Enable TLS |
| `maxConnections` | `*int32` | no | `10` | no | Connection pool size (1-100) |
| `connectionTimeout` | `*int32` | no | `30` | no | Connection timeout in seconds (5-300) |
| `instance` | `*InstanceSpec` | no | | presence immutable | Managed deployment config. See below. |

### InstanceSpec

| Field | Type | Required | Default | Immutable | Description |
|-------|------|----------|---------|-----------|-------------|
| `acceptEULA` | `bool` | yes | | no | Must be `true` |
| `image` | `*string` | no | `mcr.microsoft.com/mssql/server:2022-latest` | no | Container image |
| `edition` | `*string` | no | `Developer` | no | `Developer`, `Express`, `Standard`, `Enterprise`, `EnterpriseCore` |
| `replicas` | `*int32` | no | `1` | no | 1 = standalone, 2-5 = AG cluster |
| `saPasswordSecret` | `SecretReference` | yes | | no | Secret with `MSSQL_SA_PASSWORD` key |
| `storageSize` | `*string` | no | `10Gi` | no | PVC size per replica |
| `storageClassName` | `*string` | no | cluster default | yes | StorageClass name |
| `resources` | `*ResourceRequirements` | no | | no | CPU/memory requests and limits |
| `serviceType` | `*ServiceType` | no | `ClusterIP` | no | `ClusterIP`, `NodePort`, `LoadBalancer` |
| `nodeSelector` | `map[string]string` | no | | no | Node scheduling constraints |
| `tolerations` | `[]Toleration` | no | | no | Pod tolerations |
| `affinity` | `*Affinity` | no | | no | Pod affinity/anti-affinity |
| `topologySpreadConstraints` | `[]TopologySpreadConstraint` | no | | no | Topology spread |
| `certificates` | `*CertificateSpec` | no | | no | HADR certificate config (required for replicas > 1) |
| `availabilityGroup` | `*ManagedAGSpec` | no | | no | AG config (for replicas > 1) |

### CertificateSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mode` | `*CertificateMode` | no | `SelfSigned` | `SelfSigned` or `CertManager` |
| `issuerRef` | `*CertManagerIssuerRef` | conditional | | Required when mode is `CertManager` |
| `duration` | `*string` | no | `8760h` | Certificate validity duration |
| `renewBefore` | `*string` | no | `720h` | Renewal window before expiry |

### ManagedAGSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `agName` | `*string` | no | `{name}-ag` | AG name on SQL Server |
| `availabilityMode` | `*string` | no | `SynchronousCommit` | `SynchronousCommit` or `AsynchronousCommit` |
| `autoFailover` | `*bool` | no | `true` | Operator-managed automatic failover |
| `healthCheckInterval` | `*string` | no | `10s` | Primary health check interval |
| `failoverCooldown` | `*string` | no | `60s` | Minimum time between auto-failovers |
| `databases` | `[]string` | no | `[]` | Databases to include in the AG |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |
| `serverVersion` | `string` | SQL Server version (e.g. `16.0.4135.4`) |
| `edition` | `string` | SQL Server edition (e.g. `Enterprise`) |
| `lastConnectedTime` | `*metav1.Time` | Last successful connection |
| `host` | `string` | Effective hostname (managed Service FQDN or spec.host) |
| `readyReplicas` | `*int32` | Ready pods (managed mode only) |
| `primaryReplica` | `string` | Current primary pod (managed cluster only) |
| `certificatesReady` | `*bool` | HADR certificates provisioned (managed cluster only) |

### Print columns

```
NAME    HOST   PORT   AUTH       READY   VERSION   AGE
```

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
| `recoveryModel` | `*RecoveryModel` | no | | no | `Simple`, `Full`, `BulkLogged` |
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

**Short name:** `msbak` | **Category:** `mssql`

One-shot database backup. Executes once, then remains in `Completed` or `Failed` phase.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `server` | `ServerReference` | yes | | SQL Server connection |
| `databaseName` | `string` | yes | | Database to back up |
| `destination` | `string` | yes | | File path on the SQL Server filesystem |
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
| `backupSize` | `*int64` | Backup file size in bytes |

### Print columns

```
NAME              DATABASE   TYPE   PHASE       AGE
```

### Immutability

Spec is fully immutable after creation. To re-run a backup, delete the CR and create a new one.

---

## Restore

**Short name:** `msrestore` | **Category:** `mssql`

One-shot database restore. Executes once, then remains in `Completed` or `Failed` phase.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `server` | `ServerReference` | yes | | SQL Server connection |
| `databaseName` | `string` | yes | | Target database name |
| `source` | `string` | yes | | Backup file path on the SQL Server filesystem |
| `pointInTime` | `*string` | no | | Point-in-time restore target (ISO 8601 datetime) |
| `relocateFiles` | `[]RelocateFile` | no | | Move data/log files to different paths |

### RelocateFile

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `logicalName` | `string` | yes | Logical file name in the backup |
| `physicalName` | `string` | yes | New physical path |

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

## ScheduledBackup

**Short name:** `mssbak` | **Category:** `mssql`

Runs database backups on a cron schedule.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `server` | `ServerReference` | yes | | SQL Server connection |
| `databaseName` | `string` | yes | | Database to back up |
| `schedule` | `string` | yes | | Cron expression (e.g. `0 2 * * *`) |
| `type` | `BackupType` | no | `Full` | `Full`, `Differential`, or `Log` |
| `compression` | `*bool` | no | `false` | Enable backup compression |
| `destination` | `string` | yes | | Backup destination path (supports Go templates) |
| `suspend` | `*bool` | no | `false` | Suspend scheduled backups |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |
| `lastBackupTime` | `*metav1.Time` | When the last backup ran |
| `lastBackupResult` | `string` | `Completed` or `Failed` |
| `totalBackups` | `int32` | Total backups executed |
| `failedBackups` | `int32` | Total failed backups |

### Print columns

```
NAME            DATABASE   SCHEDULE      SUSPEND   LAST   READY   AGE
```

---

## AvailabilityGroup

**Short name:** `msag` | **Category:** `mssql`

Manages an Always On Availability Group for SQL Server high availability.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `agName` | `string` | yes | | Name of the AG on SQL Server (immutable) |
| `replicas` | `[]AGReplicaSpec` | yes (min 2) | | Replicas participating in the AG |
| `databases` | `[]AGDatabaseSpec` | no | `[]` | Databases to include in the AG |
| `listener` | `*AGListenerSpec` | no | | AG listener configuration |
| `automatedBackupPreference` | `*string` | no | `Secondary` | Where automated backups run |
| `dbFailover` | `*bool` | no | `true` | Database-level health detection |
| `clusterType` | `*string` | no | `External` | `WSFC`, `External`, or `None` |
| `autoFailover` | `*bool` | no | `false` | Operator-managed automatic failover |
| `healthCheckInterval` | `*string` | no | `10s` | Primary health check interval |
| `failoverCooldown` | `*string` | no | `60s` | Minimum time between auto-failovers |

### AGReplicaSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `serverName` | `string` | yes | | SQL Server instance name |
| `endpointURL` | `string` | yes | | Database mirroring endpoint (e.g. `TCP://sql-0:5022`) |
| `availabilityMode` | `AvailabilityMode` | no | `SynchronousCommit` | `SynchronousCommit` or `AsynchronousCommit` |
| `failoverMode` | `FailoverMode` | no | `Automatic` | `Automatic` or `Manual` |
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
| `autoFailoverCount` | `int32` | Total automatic failovers |
| `lastAutoFailoverTime` | `*metav1.Time` | Timestamp of last auto-failover |

### Print columns

```
NAME       AG     PRIMARY   READY   AGE
```

### Immutability

Only `agName` is immutable. Replicas, databases, and listener can be updated after creation.

### Deletion behavior

Dropping the CR drops the AG on SQL Server. Databases are **not** deleted -- they remain as standalone databases.

---

## AGFailover

**Short name:** `msagfo` | **Category:** `mssql`

One-shot manual failover of an Availability Group.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `agName` | `string` | yes | | Name of the AG to failover |
| `targetReplica` | `string` | yes | | Server name of the secondary to promote |
| `server` | `ServerReference` | yes | | Connection details for the target replica |
| `force` | `*bool` | no | `false` | Force failover (accepts data loss) |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `FailoverPhase` | `Pending`, `Running`, `Completed`, `Failed` |
| `conditions` | `[]metav1.Condition` | See [status conditions](status-conditions.md) |
| `observedGeneration` | `int64` | Last reconciled `metadata.generation` |
| `startTime` | `*metav1.Time` | When the failover started |
| `completionTime` | `*metav1.Time` | When the failover completed |
| `previousPrimary` | `string` | Server name of the primary before failover |
| `newPrimary` | `string` | Server name of the primary after failover |

### Print columns

```
NAME              AG     TARGET   PHASE       AGE
```

### Immutability

Spec is fully immutable after creation. To retry a failover, delete the CR and create a new one.
