# Getting Started

This guide walks you through deploying the MSSQL Kubernetes Operator and creating your first managed SQL Server objects.

## Prerequisites

- Kubernetes 1.25+
- Helm 3.x
- A SQL Server instance (2016+) accessible from the cluster
- `kubectl` configured to access your cluster

## Installation

### Via Helm

```bash
helm repo add mssql-operator https://popul.github.io/mssql-k8s-operator
helm install mssql-operator mssql-operator/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace
```

### From Source

```bash
git clone https://github.com/popul/mssql-k8s-operator.git
cd mssql-k8s-operator
make install   # Install CRDs
make deploy    # Deploy operator via Helm
```

## Quick Start (5 minutes)

### 1. Create a credentials Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: mssql-credentials
stringData:
  username: sa
  password: "YourStrong!Passw0rd"
```

### 2. Register your SQL Server

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: my-sql-server
spec:
  host: mssql.database.svc.cluster.local
  port: 1433
  credentialsSecret:
    name: mssql-credentials
  tls: false
```

Check connectivity:
```bash
kubectl get sqlservers
# NAME            HOST                                PORT   AUTH       READY   VERSION
# my-sql-server   mssql.database.svc.cluster.local   1433   SqlLogin   True    16.0.4135.4
```

### 3. Create a Database

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
spec:
  server:
    sqlServerRef: my-sql-server
  databaseName: myapp
  recoveryModel: Full
  deletionPolicy: Retain
```

### 4. Create a Login and User

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: myapp-password
stringData:
  password: "MyApp!Passw0rd"
---
apiVersion: mssql.popul.io/v1alpha1
kind: Login
metadata:
  name: myapp-login
spec:
  server:
    sqlServerRef: my-sql-server
  loginName: myapp_user
  passwordSecret:
    name: myapp-password
  serverRoles: []
---
apiVersion: mssql.popul.io/v1alpha1
kind: DatabaseUser
metadata:
  name: myapp-user
spec:
  server:
    sqlServerRef: my-sql-server
  databaseName: myapp
  userName: myapp_user
  loginRef:
    name: myapp-login
  databaseRoles:
    - db_datareader
    - db_datawriter
```

### 5. Set Up Automated Backups

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: ScheduledBackup
metadata:
  name: myapp-nightly
spec:
  server:
    sqlServerRef: my-sql-server
  databaseName: myapp
  schedule: "0 2 * * *"
  type: Full
  compression: true
  destinationTemplate: "/backups/{{.DatabaseName}}-{{.Timestamp}}.bak"
  retention:
    maxCount: 7
    maxAge: "168h"
```

### 6. Verify Everything

```bash
kubectl get mssql -A
# Lists all MSSQL resources across namespaces

kubectl get databases
# NAME       DATABASE   READY   AGE
# myapp-db   myapp      True    2m

kubectl get scheduledbackups
# NAME            DATABASE   SCHEDULE      SUSPEND   LAST                   READY
# myapp-nightly   myapp      0 2 * * *     false     2024-01-02T02:00:00Z   True
```

## What's Next?

- [CRD Reference](reference/crds.md) - Complete API documentation
- [Backup & Restore Guide](how-to/backup-restore.md) - Advanced backup scenarios
- [High Availability Guide](how-to/high-availability.md) - Always On Availability Groups
- [Observability Guide](how-to/observability.md) - Metrics, alerts, and monitoring
