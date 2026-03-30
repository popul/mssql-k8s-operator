# MSSQL Kubernetes Operator

A Kubernetes operator for managing SQL Server declaratively -- from deploying a full HA cluster to managing databases, logins, backups, and permissions via Custom Resources.

## A 3-node HA cluster in 30 lines of YAML

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
spec:
  credentialsSecret:
    name: sa-credentials
  instance:
    acceptEULA: true
    edition: Enterprise
    saPasswordSecret:
      name: mssql-sa-password
    replicas: 3
    storageSize: 50Gi
    resources:
      requests: { memory: 4Gi, cpu: "1" }
      limits:   { memory: 8Gi }
    topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: DoNotSchedule
        labelSelector:
          matchLabels:
            app.kubernetes.io/instance: mssql
    certificates:
      mode: SelfSigned
    availabilityGroup:
      agName: myag
      autoFailover: true
```

`kubectl apply` -- and the operator handles the rest:

- **3-replica StatefulSet** with persistent storage and HADR enabled
- **Self-signed CA + per-replica TLS certificates** for secure endpoint authentication
- **Always On Availability Group** created, secondaries joined, synchronous commit
- **Automatic failover** -- primary goes down, operator promotes a secondary in seconds
- **Headless + client Services**, topology-aware scheduling across zones

Then manage everything else as code:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
spec:
  server:
    sqlServerRef: mssql    # that's it -- no host, no credentials to repeat
  databaseName: myapp
```

## What it does

- **Deploy** SQL Server instances (standalone or HA cluster) directly from a CR
- **Declare** the desired state of SQL Server objects as Kubernetes CRs
- **Reconcile** continuously -- the operator converges actual state on SQL Server
- **Secure** credentials via Kubernetes Secrets, never in plain text
- **Protect** data with `deletionPolicy: Retain` by default
- **Monitor** with auto-failover, health checks, and Prometheus metrics

## Custom Resources

| CRD | Short name | Description |
|-----|-----------|-------------|
| `SQLServer` | `mssrv` | SQL Server instance (managed deployment or external connection) |
| `Database` | `msdb` | Databases on a SQL Server instance |
| `Login` | `mslogin` | SQL Server logins (SQL authentication) |
| `DatabaseUser` | `msuser` | Database users mapped to logins |
| `Schema` | `msschema` | Schemas inside a database |
| `Permission` | `msperm` | Fine-grained GRANT / DENY / REVOKE |
| `Backup` | `msbak` | One-shot database backup (Full/Differential/Log) |
| `Restore` | `msrestore` | One-shot database restore from backup file |
| `ScheduledBackup` | `mssbak` | Cron-scheduled automated backups |
| `AvailabilityGroup` | `msag` | Always On Availability Groups (HA) |
| `AGFailover` | `msagfo` | One-shot AG failover (manual/forced) |

## Quick start

```bash
# Install the operator
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system --create-namespace

# Create secrets
kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!'
kubectl create secret generic sa-credentials \
  --from-literal=username=sa --from-literal=password='YourStr0ngP@ssword!'

# Deploy a managed SQL Server
cat <<EOF | kubectl apply -f -
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
spec:
  credentialsSecret:
    name: sa-credentials
  instance:
    acceptEULA: true
    saPasswordSecret:
      name: mssql-sa-password
    storageSize: 10Gi
EOF

# Wait for it to be ready
kubectl get sqlsrv mssql -w

# Create a database
cat <<EOF | kubectl apply -f -
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
spec:
  server:
    sqlServerRef: mssql
  databaseName: myapp
EOF

kubectl get msdb
```

## Documentation

| Type | Content |
|------|---------|
| **[Tutorial](docs/tutorials/getting-started.md)** | Deploy the operator and create your first database end-to-end |
| **How-to guides** | |
| [Install](docs/how-to/install.md) | Install, upgrade, and configure the operator |
| [Deploy SQL Server](docs/how-to/deploy-sql-server.md) | Deploy a managed SQL Server instance via the SQLServer CR |
| [High availability](docs/how-to/high-availability.md) | Set up a multi-replica HA cluster with auto-failover |
| [Manage databases](docs/how-to/manage-databases.md) | Create, update, delete databases |
| [Manage logins & users](docs/how-to/manage-logins-users.md) | Create logins, users, rotate passwords |
| [Manage schemas & permissions](docs/how-to/manage-schemas-permissions.md) | Create schemas, grant/deny permissions |
| [Backup & restore](docs/how-to/backup-restore.md) | Backup and restore databases |
| [Version & edition](docs/how-to/sql-server-version-edition.md) | Change SQL Server version or edition |
| [Manual deployment](docs/how-to/manual-sql-server-deployment.md) | Deploy SQL Server yourself and register as external |
| [Observability](docs/how-to/observability.md) | Metrics, alerts, events, structured logging |
| [Troubleshoot](docs/how-to/troubleshoot.md) | Diagnose and fix common issues |
| **Reference** | |
| [CRD reference](docs/reference/crds.md) | All CRD fields, types, and defaults |
| [Status conditions](docs/reference/status-conditions.md) | Conditions, reasons, and events |
| [Helm values](docs/reference/helm-values.md) | All chart configuration options |
| **[Architecture](docs/explanation/architecture.md)** | Reconciliation loop, design decisions, security model |

## Requirements

- Kubernetes 1.27+
- SQL Server 2019+
- Helm 3.x

## License

Apache License 2.0
