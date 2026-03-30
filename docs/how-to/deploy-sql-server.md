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

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
  namespace: mssql
spec:
  instance:
    acceptEULA: true
    edition: Enterprise
    saPasswordSecret:
      name: mssql-sa-password
    replicas: 3
    storageSize: 50Gi
    resources:
      requests:
        memory: 4Gi
        cpu: "1"
      limits:
        memory: 8Gi
    nodeSelector:
      disktype: ssd
    topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: ScheduleAnyway
        labelSelector:
          matchLabels:
            app.kubernetes.io/instance: mssql
    certificates:
      mode: SelfSigned
    availabilityGroup:
      agName: myag
      availabilityMode: SynchronousCommit
      autoFailover: true
```

> For HA setup details (failover testing, monitoring, troubleshooting), see [High Availability](high-availability.md).

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

## Step 4: Use it with other CRs

Reference the `SQLServer` CR by name in your Database, Login, and other CRs:

```yaml
spec:
  server:
    sqlServerRef: mssql
  databaseName: myapp
```

No need to specify host, port, or credentials -- the operator resolves everything from the `SQLServer` CR.

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
| **Resources** | Always set memory limits. SQL Server uses all available memory by default |
| **Edition** | Set `edition` for production. See [version and edition guide](sql-server-version-edition.md) |
| **TLS** | Enable TLS for production connections |
| **Backups** | Use the operator's `ScheduledBackup` CR |
| **High availability** | Use `replicas: 3` for production HA. See [HA guide](high-availability.md) |
| **Storage** | Use a fast StorageClass (SSD). `storageClassName` is immutable |
