# MSSQL Kubernetes Operator

A Kubernetes operator for managing SQL Server objects (databases, logins, users, schemas, permissions) declaratively via Custom Resources.

## What it does

- Declare the desired state of SQL Server objects as Kubernetes CRs
- The operator continuously reconciles the actual state on SQL Server
- Credentials are managed securely via Kubernetes Secrets
- Supports `deletionPolicy: Retain` (default) to prevent accidental data loss

## Custom Resources

| CRD | Short name | Description |
|-----|-----------|-------------|
| `Database` | `msdb` | Databases on a SQL Server instance |
| `Login` | `mslogin` | SQL Server logins (SQL authentication) |
| `DatabaseUser` | `msuser` | Database users mapped to logins |
| `Schema` | `msschema` | Schemas inside a database |
| `Permission` | `msperm` | Fine-grained GRANT / DENY / REVOKE |

## Quick start

```bash
# Install the operator
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system --create-namespace

# Create a credentials Secret
kubectl create secret generic mssql-sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStrongPassword!'

# Create a database
cat <<EOF | kubectl apply -f -
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
EOF

# Check status
kubectl get msdb
```

## Documentation

| Type | Content |
|------|---------|
| **[Tutorial](docs/tutorials/getting-started.md)** | Deploy the operator and create your first database end-to-end |
| **How-to guides** | |
| [Install](docs/how-to/install.md) | Install, upgrade, and configure the operator |
| [Manage databases](docs/how-to/manage-databases.md) | Create, update, delete databases |
| [Manage logins & users](docs/how-to/manage-logins-users.md) | Create logins, users, rotate passwords |
| [Manage schemas & permissions](docs/how-to/manage-schemas-permissions.md) | Create schemas, grant/deny permissions |
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

See [LICENSE](LICENSE).
