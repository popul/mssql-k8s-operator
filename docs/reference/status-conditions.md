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

| Reason | Status | Description | CRDs |
|--------|--------|-------------|------|
| `Ready` | `True` | Resource is reconciled and matches desired state | All |
| `ConnectionFailed` | `False` | Cannot connect to SQL Server | All |
| `SecretNotFound` | `False` | Referenced credentials Secret does not exist | All |
| `InvalidCredentialsSecret` | `False` | Secret is missing `username` or `password` key | All |
| `CollationChangeNotSupported` | `False` | Collation drift detected (immutable field) | Database |
| `DatabaseProvisioning` | `False` | Database is being created | Database |
| `LoginInUse` | `False` | Login has dependent database users, cannot delete | Login |
| `InvalidServerRole` | `False` | Server role name is invalid | Login |
| `LoginRefNotFound` | `False` | Referenced Login CR does not exist | DatabaseUser |
| `LoginNotReady` | `False` | Referenced Login CR is not Ready | DatabaseUser |
| `UserOwnsObjects` | `False` | User owns database objects, cannot delete | DatabaseUser |
| `SchemaNotEmpty` | `False` | Schema contains objects, cannot drop | Schema |

## Events

The operator emits Kubernetes events on significant actions. View them with `kubectl describe`.

### Database events

| Event | Type | Description |
|-------|------|-------------|
| `DatabaseCreated` | Normal | Database was created on SQL Server |
| `DatabaseDropped` | Normal | Database was dropped on SQL Server |
| `DatabaseOwnerUpdated` | Normal | `ALTER AUTHORIZATION` was executed |
| `ConnectionFailed` | Warning | Cannot connect to SQL Server |

### Login events

| Event | Type | Description |
|-------|------|-------------|
| `LoginCreated` | Normal | Login was created |
| `LoginDropped` | Normal | Login was dropped |
| `LoginPasswordRotated` | Normal | Password was updated |
| `LoginDefaultDatabaseUpdated` | Normal | Default database was changed |
| `ServerRoleAdded` | Normal | Login was added to a server role |
| `ServerRoleRemoved` | Normal | Login was removed from a server role |

### DatabaseUser events

| Event | Type | Description |
|-------|------|-------------|
| `UserCreated` | Normal | User was created in the database |
| `UserDropped` | Normal | User was dropped |
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

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `mssql_operator_reconcile_duration_seconds` | Histogram | `resource_type` | Time spent in `Reconcile()` |
| `mssql_operator_reconcile_total` | Counter | `resource_type`, `result` | Total reconciliations |
| `mssql_operator_reconcile_errors_total` | Counter | `resource_type`, `reason` | Error count |

## Health endpoints

| Endpoint | Description |
|----------|-------------|
| `/healthz` | Liveness probe -- returns 200 if the process is alive |
| `/readyz` | Readiness probe -- returns 200 if the manager is ready |
