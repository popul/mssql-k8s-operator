# Deploy SQL Server in Kubernetes

This guide shows how to deploy a SQL Server instance using the `SQLServer` CR. The operator creates and manages the StatefulSet, Services, PVCs, and (for multi-replica setups) certificates and Availability Groups automatically.

> For connecting to an existing SQL Server or managing the deployment yourself, see [Manual deployment & external mode](manual-sql-server-deployment.md).

## Prerequisites

- The mssql-k8s-operator installed ([Installation guide](install.md))
- `kubectl` configured

## Step 1: Create Secret

```bash
kubectl create namespace mssql

# SA password — used by both the SQL Server container and the operator
kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!' \
  -n mssql
```

> **Note**: In managed mode, if `credentialsSecret` is not set on the SQLServer CR, the operator
> automatically connects using `sa` with the password from `saPasswordSecret`. A single secret is
> sufficient. If you need a dedicated operator account (recommended for production), create a
> separate credentials secret with `username` and `password` keys and set `credentialsSecret` on the CR.

## Step 2: Create the SQLServer CR

### Standalone instance (1 replica)

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
  namespace: mssql
spec:
  instance:
    acceptEULA: true
    saPasswordSecret:
      name: mssql-sa-password
    edition: Developer
    storageSize: 10Gi
    resources:
      requests:
        memory: 2Gi
        cpu: 500m
      limits:
        memory: 4Gi
```

### HA cluster (3 replicas with Availability Group)

For multi-replica deployments with Availability Groups, see the dedicated [High Availability guide](high-availability.md).

## Step 3: Apply and verify

```bash
kubectl apply -f sqlserver.yaml

# Watch the status
kubectl get sqlsrv mssql -n mssql -w
```

The operator creates:
- A **StatefulSet** with the requested replicas
- A **headless Service** (`mssql-headless`) for inter-pod DNS
- A **client Service** (`mssql`) for application access
- **PVCs** per replica via VolumeClaimTemplates
- **HADR certificates** (if replicas > 1)
- An **Availability Group** (if replicas > 1)

## Step 4: Create a database

```bash
cat <<EOF | kubectl apply -f -
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
  namespace: mssql
spec:
  server:
    sqlServerRef: mssql
  databaseName: myapp
EOF
```

```bash
kubectl get msdb -n mssql
# NAME       DATABASE   READY   AGE
# myapp-db   myapp      True    5s
```

The `sqlServerRef` references the `SQLServer` CR by name -- no need to repeat host, port, or credentials. This works for all CRs: `Database`, `Login`, `DatabaseUser`, `Schema`, `Permission`, `Backup`, etc.

## Step 5: Connect to the database

**From inside the cluster** (via the pod):

```bash
kubectl exec -it mssql-0 -n mssql -- /opt/mssql-tools18/bin/sqlcmd \
  -S localhost -U sa -P 'YourStr0ngP@ssword!' -d myapp -C
```

**From your machine** (via port-forward):

```bash
kubectl port-forward svc/mssql 1433:1433 -n mssql
```

Then connect with any SQL client:

```bash
# sqlcmd (Microsoft CLI)
sqlcmd -S localhost -U sa -P 'YourStr0ngP@ssword!' -d myapp

# Azure Data Studio, DBeaver, DataGrip...
# Host: localhost, Port: 1433, User: sa, Database: myapp
```

## Instance spec reference

| Field | Default | Description |
|---|---|---|
| `acceptEULA` | required | Must be `true` |
| `image` | `mssql/server:2022-latest` | SQL Server container image |
| `edition` | `Developer` | `Developer`, `Express`, `Standard`, `Enterprise` |
| `replicas` | `1` | 1 = standalone, 2-5 = AG cluster |
| `saPasswordSecret` | required | Secret with `MSSQL_SA_PASSWORD` key |
| `storageSize` | `10Gi` | PVC size per replica |
| `storageClassName` | cluster default | StorageClass (immutable after creation) |
| `resources` | none | CPU/memory requests and limits |
| `serviceType` | `ClusterIP` | `ClusterIP`, `NodePort`, `LoadBalancer` |
| `nodeSelector` | none | Node scheduling constraints |
| `tolerations` | none | Pod tolerations |
| `affinity` | none | Pod affinity/anti-affinity |
| `topologySpreadConstraints` | none | Topology spread |
| `certificates.mode` | `SelfSigned` | `SelfSigned` or `CertManager` |
| `certificates.issuerRef` | none | Required if mode is `CertManager` |
| `availabilityGroup.agName` | `{name}-ag` | AG name on SQL Server |
| `availabilityGroup.availabilityMode` | `SynchronousCommit` | Sync mode |
| `availabilityGroup.autoFailover` | `true` | Operator-managed failover |
| `config` | auto memory | Raw `mssql.conf` content ([see section](#configuring-sql-server-mssqlconf)) |

## Configuring SQL Server (mssql.conf)

The `config` field accepts raw `mssql.conf` content in INI format:

```yaml
spec:
  instance:
    config: |
      [memory]
      memorylimitmb = 4096

      [network]
      forceencryption = 1

      [traceflag]
      traceflag0 = 1222
      traceflag1 = 3226
    resources:
      limits:
        memory: 8Gi
```

The operator creates a ConfigMap and mounts it as `/var/opt/mssql/mssql.conf`.

### Auto-calculated memory limit

If `memorylimitmb` is not set but `resources.limits.memory` is defined, the operator automatically sets it to **80% of the container memory limit**. This prevents SQL Server from consuming all available memory and getting OOMKilled.

For example, with `limits.memory: 4Gi`, the operator sets `memorylimitmb = 3276`.

### Common options

| Section | Key | Example | Description |
|---|---|---|---|
| `[memory]` | `memorylimitmb` | `4096` | Max memory in MB (auto-set if omitted) |
| `[network]` | `forceencryption` | `1` | Force TLS for all connections |
| `[network]` | `tcpport` | `1433` | TCP listen port |
| `[sqlagent]` | `enabled` | `true` | Enable SQL Server Agent |
| `[traceflag]` | `traceflag0` | `1222` | Deadlock monitoring |
| `[traceflag]` | `traceflag1` | `3226` | Suppress backup log messages |
| `[collation]` | `sqlcollation` | `Latin1_General_CI_AS` | Default instance collation |
| `[filelocation]` | `defaultdatadir` | `/var/opt/mssql/data` | Default data file path |
| `[filelocation]` | `defaultlogdir` | `/var/opt/mssql/log` | Default log file path |
| `[filelocation]` | `defaultbackupdir` | `/var/opt/mssql/backup` | Default backup path |

Full reference: [Microsoft docs](https://learn.microsoft.com/en-us/sql/linux/sql-server-linux-configure-mssql-conf).

## Exposing SQL Server outside the cluster

Set `serviceType: LoadBalancer` or `NodePort`:

```yaml
instance:
  serviceType: LoadBalancer
```

> **Warning**: never expose SQL Server to the public internet without TLS and strong credentials.

## Production considerations

| Topic | Recommendation |
|---|---|
| **Resources** | Always set memory limits. The operator auto-sets `memorylimitmb` to 80% of the limit |
| **Edition** | Set `edition` for production. See [version and edition guide](sql-server-version-edition.md) |
| **TLS** | Enable TLS for production connections |
| **Backups** | Use the operator's `ScheduledBackup` CR |
| **High availability** | Use `replicas: 3` for production HA. See [HA guide](high-availability.md) |
| **Storage** | Use a fast StorageClass (SSD). `storageClassName` is immutable |
