# Status Conditions Reference

All CRDs use a single condition type: `Ready`.

## Condition fields

| Field | Description |
|-------|-------------|
| `type` | Always `Ready` |
| `status` | `True`, `False`, or `Unknown` |
| `reason` | PascalCase, machine-readable (see table below) |
| `message` | Human-readable description |
| `lastTransitionTime` | When `status` last changed |
| `observedGeneration` | The `metadata.generation` this condition was computed from |

## Reasons

### Common (all CRDs)

| Reason | Status | Description |
|--------|--------|-------------|
| `Ready` | `True` | Resource is reconciled and matches desired state |
| `ConnectionFailed` | `False` | Cannot connect to SQL Server |
| `SecretNotFound` | `False` | Referenced credentials Secret does not exist |
| `InvalidCredentialsSecret` | `False` | Secret is missing `username` or `password` key |

### SQLServer (managed mode)

| Reason | Status | Description |
|--------|--------|-------------|
| `DeploymentProvisioning` | `False` | StatefulSet pods are not yet ready |
| `DeploymentReady` | `True` | All infrastructure is ready |
| `EULANotAccepted` | `False` | `instance.acceptEULA` is not `true` |
| `CertificatesProvisioning` | `False` | HADR certificates are being generated/distributed |
| `AGProvisioning` | `False` | Availability Group is being created/configured |

### Database

| Reason | Status | Description |
|--------|--------|-------------|
| `DatabaseProvisioning` | `False` | Database is being created |
| `CollationChangeNotSupported` | `False` | Collation drift detected (immutable field) |

### Login

| Reason | Status | Description |
|--------|--------|-------------|
| `LoginInUse` | `False` | Login has dependent database users, cannot delete |
| `InvalidServerRole` | `False` | Server role name is invalid |

### DatabaseUser

| Reason | Status | Description |
|--------|--------|-------------|
| `LoginRefNotFound` | `False` | Referenced Login CR does not exist |
| `LoginNotReady` | `False` | Referenced Login CR is not Ready |
| `UserOwnsObjects` | `False` | User owns database objects, cannot delete |

### Schema

| Reason | Status | Description |
|--------|--------|-------------|
| `SchemaNotEmpty` | `False` | Schema contains objects, cannot drop |

### Backup / Restore

| Reason | Status | Description |
|--------|--------|-------------|
| `BackupRunning` | `False` | Backup is in progress |
| `BackupCompleted` | `True` | Backup completed successfully |
| `BackupFailed` | `False` | Backup failed |
| `RestoreRunning` | `False` | Restore is in progress |
| `RestoreCompleted` | `True` | Restore completed successfully |
| `RestoreFailed` | `False` | Restore failed |
| `DatabaseNotFound` | `False` | Database does not exist (for backup) |

### AvailabilityGroup

| Reason | Status | Description |
|--------|--------|-------------|
| `AGProvisioning` | `False` | AG is being created or configured |
| `AGReady` | `True` | AG is operational with primary and secondaries |
| `PrimaryUnreachable` | `False` | Cannot connect to the primary replica |
| `SplitBrainDetected` | `False` | Two or more replicas report PRIMARY simultaneously |
| `FencingExecuted` | `False` | Soft fencing executed (rogue demoted to SECONDARY) |
| `HardFencingExecuted` | `False` | Hard fencing executed (AG dropped on rogue, will rejoin) |
| `FencingFailed` | `False` | Fencing SQL command failed (traffic already cut via label) |
| `FencingExhausted` | `False` | Circuit-breaker: fencing failed repeatedly, manual intervention required |
| `PrimaryChangedExternally` | `True` | Primary changed outside the operator (status corrected, no fencing) |

## Events

The operator emits Kubernetes events on CRs for significant actions. View them with `kubectl describe`.

### SQLServer events

| Event | Type | Description |
|-------|------|-------------|
| `StatefulSetCreated` | Normal | StatefulSet was created (managed mode) |
| `ServiceCreated` | Normal | Service was created (managed mode) |
| `CertificatesReady` | Normal | HADR certificates provisioned |
| `AGCreated` | Normal | Availability Group created (managed mode) |
| `AutoFailoverCompleted` | Normal | Auto-failover completed (managed mode) |
| `ConnectionFailed` | Warning | Cannot connect to SQL Server |

### Database events

| Event | Type | Description |
|-------|------|-------------|
| `DatabaseCreated` | Normal | Database was created on SQL Server |
| `DatabaseDropped` | Normal | Database was dropped on SQL Server |
| `DatabaseOwnerUpdated` | Normal | `ALTER AUTHORIZATION` was executed |
| `RecoveryModelUpdated` | Normal | Recovery model was changed |
| `ConnectionFailed` | Warning | Cannot connect to SQL Server |

### Login events

| Event | Type | Description |
|-------|------|-------------|
| `LoginCreated` | Normal | Login was created |
| `LoginDropped` | Normal | Login was dropped |
| `LoginPasswordRotated` | Normal | Password was updated |
| `ServerRoleAdded` | Normal | Login was added to a server role |
| `ServerRoleRemoved` | Normal | Login was removed from a server role |

### DatabaseUser events

| Event | Type | Description |
|-------|------|-------------|
| `DatabaseUserCreated` | Normal | User was created in the database |
| `DatabaseUserDropped` | Normal | User was dropped |
| `DatabaseRoleAdded` | Normal | User was added to a database role |
| `DatabaseRoleRemoved` | Normal | User was removed from a database role |

### Schema events

| Event | Type | Description |
|-------|------|-------------|
| `SchemaCreated` | Normal | Schema was created |
| `SchemaDropped` | Normal | Schema was dropped |
| `SchemaOwnerUpdated` | Normal | Schema owner was changed |
| `SchemaNotEmpty` | Warning | Schema contains objects, deletion blocked |

### Permission events

| Event | Type | Description |
|-------|------|-------------|
| `PermissionGranted` | Normal | Permission was GRANTed |
| `PermissionDenied` | Normal | Permission was DENYed |
| `PermissionRevoked` | Normal | Permission was REVOKEd |
| `PermissionsRevoked` | Normal | All permissions revoked on deletion |

### Backup / Restore events

| Event | Type | Description |
|-------|------|-------------|
| `BackupStarted` | Normal | Backup started |
| `BackupCompleted` | Normal | Backup completed successfully |
| `BackupFailed` | Warning | Backup failed |
| `RestoreStarted` | Normal | Restore started |
| `RestoreCompleted` | Normal | Restore completed successfully |
| `RestoreFailed` | Warning | Restore failed |

### AvailabilityGroup events

| Event | Type | Description |
|-------|------|-------------|
| `AGCreated` | Normal | AG was created on SQL Server |
| `AGDropped` | Normal | AG was dropped on SQL Server |
| `ReplicaJoined` | Normal | Secondary replica joined the AG |
| `DatabaseJoined` | Normal | Database was added to the AG |
| `AutoFailoverCompleted` | Normal | Auto-failover completed |
| `AutoFailoverFailed` | Warning | Auto-failover failed |
| `PrimaryUnreachable` | Warning | Cannot connect to primary replica |

### AGFailover events

| Event | Type | Description |
|-------|------|-------------|
| `FailoverStarted` | Normal | Manual failover started |
| `FailoverCompleted` | Normal | Manual failover completed |
| `FailoverFailed` | Warning | Manual failover failed |

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `mssql_operator_reconcile_duration_seconds` | Histogram | `controller` | Time spent in `Reconcile()` |
| `mssql_operator_reconcile_total` | Counter | `controller`, `result` | Total reconciliations |
| `mssql_operator_reconcile_errors_total` | Counter | `controller`, `reason` | Error count |
| `mssql_operator_managed_resources` | Gauge | `type`, `namespace` | Number of managed SQL Server resources |
| `mssql_database_ready` | Gauge | `name`, `namespace` | Database CR is Ready (1/0) |
| `mssql_login_ready` | Gauge | `name`, `namespace` | Login CR is Ready (1/0) |
| `mssql_server_connected` | Gauge | `name`, `namespace` | SQLServer can be reached (1/0) |
| `mssql_backup_last_success_timestamp` | Gauge | `name`, `namespace` | Unix timestamp of last successful scheduled backup |
| `mssql_scheduled_backup_total` | Gauge | `name`, `namespace`, `result` | Total backups from a scheduled backup |
| `mssql_ag_replica_synchronized` | Gauge | `ag`, `replica`, `namespace` | AG replica is synchronized (1/0) |

## Health endpoints

| Endpoint | Description |
|----------|-------------|
| `/healthz` | Liveness probe -- returns 200 if the process is alive |
| `/readyz` | Readiness probe -- returns 200 if the manager is ready |
