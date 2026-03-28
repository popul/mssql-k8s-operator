# MSSQL Kubernetes Operator — User Guide

## Overview

The MSSQL Kubernetes Operator manages SQL Server objects (databases, logins, users) declaratively via Custom Resources. It continuously reconciles the desired state in Kubernetes with the actual state on SQL Server.

## Prerequisites

- Kubernetes 1.27+
- Helm 3.x
- A running SQL Server 2019+ instance accessible from the cluster
- `kubectl` configured with cluster access

## Installation

### Install with Helm

```bash
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace
```

### Verify

```bash
kubectl get pods -n mssql-operator-system
kubectl get mssql  # lists all MSSQL resources across namespaces
```

### Configuration

Key Helm values:

| Value | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Number of operator replicas (2+ for HA) |
| `leaderElection.enabled` | `true` | Enable leader election for HA |
| `metrics.enabled` | `true` | Enable Prometheus metrics |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus ServiceMonitor |
| `networkPolicy.enabled` | `false` | Restrict operator network access |

See `charts/mssql-operator/values.yaml` for all options.

## Creating a Credentials Secret

The operator connects to SQL Server using credentials stored in Kubernetes Secrets. Create one before creating any CR:

```bash
kubectl create secret generic mssql-sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStrongPassword!'
```

## CRD: Database

Manages SQL Server databases.

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
spec:
  server:
    host: mssql.database.svc.cluster.local
    port: 1433                                  # optional, defaults to 1433
    tls: false                                  # optional, defaults to false
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
  collation: SQL_Latin1_General_CP1_CI_AS       # optional, immutable after creation
  owner: myapp_user                             # optional, mutable
  deletionPolicy: Retain                        # Retain (default) or Delete
```

### Behavior

- **Create**: if the database does not exist, it is created.
- **Update**: owner changes are applied. Collation is immutable after creation (SQL Server limitation).
- **Delete**: with `deletionPolicy: Delete`, the database is dropped. With `Retain` (default), the database is kept.

### Short name

```bash
kubectl get msdb          # short for databases.mssql.popul.io
```

## CRD: Login

Manages SQL Server logins (SQL authentication).

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Login
metadata:
  name: myapp-login
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  loginName: myapp_user
  passwordSecret:
    name: myapp-login-password                  # Secret with key "password"
  defaultDatabase: myapp                        # optional
  serverRoles:                                  # optional
    - dbcreator
  deletionPolicy: Retain
```

### Password Rotation

Update the referenced Secret's `password` key. The operator detects the Secret change and calls `ALTER LOGIN ... WITH PASSWORD = ...` automatically.

### Short name

```bash
kubectl get mslogin       # short for logins.mssql.popul.io
```

## CRD: DatabaseUser

Manages database users mapped to SQL Server logins.

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: DatabaseUser
metadata:
  name: myapp-dbuser
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
  userName: myapp_user
  loginRef:
    name: myapp-login                           # references a Login CR
  databaseRoles:                                # optional
    - db_datareader
    - db_datawriter
```

### Deletion

The user is always dropped on CR deletion (no `deletionPolicy` field). If the user owns objects in the database, deletion is blocked until ownership is transferred.

### Short name

```bash
kubectl get msuser        # short for databaseusers.mssql.popul.io
```

## Checking Status

All CRs report their state via the `Ready` condition:

```bash
kubectl get msdb myapp-db -o jsonpath='{.status.conditions[0]}'
```

| Ready | Meaning |
|---|---|
| `True` | Resource is reconciled and matches desired state |
| `False` | Error — check `reason` and `message` for details |

Common reasons:

| Reason | Description |
|---|---|
| `Ready` | Resource is healthy |
| `ConnectionFailed` | Cannot reach SQL Server |
| `SecretNotFound` | Referenced Secret does not exist |
| `InvalidCredentialsSecret` | Secret is missing required keys |
| `CollationChangeNotSupported` | Collation drift detected (immutable) |
| `LoginInUse` | Login has dependent database users |
| `UserOwnsObjects` | User owns objects in the database |

## Events

The operator emits Kubernetes events on significant actions:

```bash
kubectl describe msdb myapp-db
```

Example events: `DatabaseCreated`, `DatabaseDropped`, `LoginPasswordRotated`, `DatabaseRoleAdded`.

## Observability

### Metrics

Prometheus metrics are exposed on port 8080 (configurable):

| Metric | Description |
|---|---|
| `mssql_operator_reconcile_duration_seconds` | Reconciliation duration by resource type |
| `mssql_operator_reconcile_total` | Total reconciliations by type and result |
| `mssql_operator_reconcile_errors_total` | Error count by type and reason |

Enable `ServiceMonitor` in Helm values for automatic Prometheus discovery.

### Health

- `/healthz` — liveness probe
- `/readyz` — readiness probe

### Logs

Structured JSON logs via `zap`. Set log level via controller-runtime flags.

## Troubleshooting

### CR stuck in `Ready=False`

```bash
kubectl describe msdb myapp-db
# Check Events and Status.Conditions for the reason
```

### CR stuck in `Terminating`

Check if:
- A `Login` has dependent `DatabaseUser` CRs — delete the users first
- A `DatabaseUser` owns objects — transfer ownership in SQL Server

The operator requeues periodically and will complete deletion once the blocker is resolved.

### Connection errors

1. Verify the SQL Server is reachable from the cluster:
   ```bash
   kubectl run test-sql --rm -it --image=mcr.microsoft.com/mssql-tools -- \
     /opt/mssql-tools/bin/sqlcmd -S mssql.database.svc.cluster.local -U sa -P 'password'
   ```
2. Check the Secret exists and has `username` and `password` keys
3. Check `NetworkPolicy` if enabled — ensure SQL Server port 1433 is allowed

### Operator not reconciling

1. Check operator pod logs: `kubectl logs -n mssql-operator-system deploy/mssql-operator`
2. Verify CRDs are installed: `kubectl get crd databases.mssql.popul.io`
3. Check RBAC: `kubectl auth can-i get secrets --as=system:serviceaccount:mssql-operator-system:mssql-operator`
