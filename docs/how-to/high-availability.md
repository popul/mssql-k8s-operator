# Set up a SQL Server HA cluster

This guide walks you through deploying a highly available SQL Server cluster with automatic failover using the `SQLServer` CR.

> For managing the StatefulSet and certificates yourself, see [Manual deployment](manual-sql-server-deployment.md).

## Architecture

```
                         Kubernetes Cluster
    +--------------------------------------------------------------+
    |                                                              |
    |   Operator (mssql-operator)                                  |
    |   - Creates StatefulSet, Services, Certs                     |
    |   - Configures Availability Group                            |
    |   - Labels pods: mssql.popul.io/role=primary|secondary      |
    |   - Monitors replicas every 10s                              |
    |   - Auto-failover on primary failure + label swap            |
    |                                                              |
    |   +--------+   HADR    +--------+   HADR    +--------+      |
    |   | sql-0  |<-- TCP -->| sql-1  |<-- TCP -->| sql-2  |      |
    |   |PRIMARY |   5022    |SECONDARY|   5022   |SECONDARY|     |
    |   +--------+           +--------+           +--------+      |
    |       |                    |                    |             |
    |   +---+----+          +---+--------------------+---+         |
    |   | mssql  |          | mssql-readonly             |         |
    |   | (R/W)  |          | (Read-Only)                |         |
    |   | role=  |          | role=secondary             |         |
    |   | primary|          +----------------------------+         |
    |   +--------+                                                 |
    |                                                              |
    |   +--------------------------------------------------+       |
    |   |         mssql-headless (Headless Service)        |       |
    |   +--------------------------------------------------+       |
    +--------------------------------------------------------------+
```

### Service routing

The operator creates **three Kubernetes Services** for HA clusters:

| Service | Name | Selector | Purpose |
|---|---|---|---|
| **Read-Write** | `{name}` | `role=primary` | Client connections (writes + reads) |
| **Read-Only** | `{name}-readonly` | `role=secondary` | Read-scale queries (`ApplicationIntent=ReadOnly`) |
| **Headless** | `{name}-headless` | instance labels | Inter-replica HADR + individual pod DNS |

The operator dynamically labels each pod with `mssql.popul.io/role=primary` or `mssql.popul.io/role=secondary`. On failover, labels are swapped so the Services automatically route to the new primary/secondaries without DNS changes.

## Prerequisites

- A Kubernetes cluster with at least 2 nodes (3 recommended for zone spread)
- The mssql-k8s-operator installed ([Installation guide](install.md))
- `kubectl` configured

## Step 1: Create Secret

```bash
kubectl create namespace mssql

kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!' \
  -n mssql
```

## Step 2: Create the SQLServer CR

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
  namespace: mssql
spec:
  instance:
    acceptEULA: true
    edition: Enterprise          # or Developer for non-prod
    saPasswordSecret:
      name: mssql-sa-password
    replicas: 3
    storageSize: 50Gi
    storageClassName: fast-ssd   # optional, uses cluster default if omitted
    resources:
      requests:
        memory: 4Gi
        cpu: "1"
      limits:
        memory: 8Gi

    # Spread pods across zones
    topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: DoNotSchedule
        labelSelector:
          matchLabels:
            app.kubernetes.io/instance: mssql

    # TLS certificates for HADR endpoints
    certificates:
      mode: SelfSigned           # operator generates CA + per-replica certs

    # Availability Group configuration
    availabilityGroup:
      agName: myag
      availabilityMode: SynchronousCommit
      autoFailover: true
      healthCheckInterval: "10s"
      failoverCooldown: "60s"
```

```bash
kubectl apply -f sqlserver.yaml
```

The operator automatically:
1. Creates a StatefulSet with 3 replicas and HADR enabled
2. Creates headless + client Services
3. Generates a self-signed CA and per-replica certificates
4. Distributes certificates to each SQL Server instance
5. Creates the Availability Group on the primary
6. Joins the secondaries to the AG
7. Starts health monitoring for auto-failover

## Step 3: Verify the cluster

```bash
# Watch deployment progress
kubectl get sqlsrv mssql -n mssql -w

# Check detailed status
kubectl describe sqlsrv mssql -n mssql
```

Expected status:

```
Status:
  Ready:              True
  Server Version:     16.0.4135.4
  Edition:            Enterprise
  Host:               mssql.mssql.svc.cluster.local
  Ready Replicas:     3
  Primary Replica:    mssql-0
  Certificates Ready: true
```

## Step 4: Create databases on the HA cluster

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
  namespace: mssql
spec:
  server:
    sqlServerRef: mssql
  databaseName: myapp
```

## Connecting your application

### Read-write connections (primary)

Use the main service DNS name. This always routes to the current primary:

```
Server=mssql.mssql.svc.cluster.local,1433
```

### Read-only connections (secondaries)

Use the read-only service for read-scale workloads. This routes to secondaries only:

```
Server=mssql-readonly.mssql.svc.cluster.local,1433;ApplicationIntent=ReadOnly
```

### Verifying routing

```bash
# Check pod roles
kubectl get pods -n mssql -l app.kubernetes.io/instance=mssql \
  -o custom-columns='NAME:.metadata.name,ROLE:.metadata.labels.mssql\.popul\.io/role'

# Check service endpoints
kubectl get endpoints mssql -n mssql
kubectl get endpoints mssql-readonly -n mssql
```

### Failover behavior

On failover (automatic or manual), the operator:

1. Promotes the target secondary via `ALTER AVAILABILITY GROUP ... FAILOVER`
2. Updates `status.primaryReplica` on the AG and SQLServer CRs
3. Patches pod labels: new primary gets `mssql.popul.io/role=primary`, old primary gets `secondary`
4. Kubernetes automatically updates Service endpoints within seconds

Your application connection string does not change. The read-write service transparently follows the primary.

## Test automatic failover

Simulate a primary failure:

```bash
# Check which pod is primary
kubectl get sqlsrv mssql -n mssql -o jsonpath='{.status.primaryReplica}'
# mssql-0

# Kill SQL Server on the primary
kubectl exec mssql-0 -n mssql -- /opt/mssql-tools18/bin/sqlcmd \
  -S localhost -U sa -P 'YourStr0ngP@ssword!' \
  -Q "SHUTDOWN WITH NOWAIT" -C -No

# Watch the failover
kubectl get sqlsrv mssql -n mssql -w
```

Within 10-30 seconds:

```
Events:
  Type     Reason                  Message
  ----     ------                  -------
  Warning  ConnectionFailed        failed to connect to primary replica
  Normal   AutoFailoverCompleted   auto-failover to mssql-1 completed
```

When `mssql-0` restarts (automatic via the StatefulSet), it rejoins as a secondary.

## Manual failover

For planned maintenance, use the `AGFailover` CR. First, create a credentials secret (AGFailover uses inline connection, not `sqlServerRef`):

```bash
kubectl create secret generic sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStr0ngP@ssword!' \
  -n mssql
```

Then apply the failover:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: AGFailover
metadata:
  name: failover-to-mssql2
  namespace: mssql
spec:
  agName: myag
  targetReplica: mssql-2
  force: true
  server:
    host: mssql-2.mssql-headless.mssql.svc.cluster.local
    credentialsSecret:
      name: sa-credentials
```

```bash
kubectl apply -f failover.yaml
kubectl get msagfo failover-to-mssql2 -n mssql
# NAME                 PHASE       AGE
# failover-to-mssql2   Completed   5s
```

## Using cert-manager

If you use [cert-manager](https://cert-manager.io) in your cluster, you can let it manage the HADR certificates:

```yaml
instance:
  certificates:
    mode: CertManager
    issuerRef:
      name: my-cluster-issuer
      kind: ClusterIssuer
```

## How auto-failover works

1. The operator pings the primary every `healthCheckInterval` (default: 10s)
2. If the ping fails, it enters the auto-failover path
3. It acquires a **Kubernetes Lease** (`ag-failover-<agName>`) to prevent split-brain
4. It connects to each secondary and checks its role
5. If a secondary is reachable, it executes `ALTER AVAILABILITY GROUP ... FORCE_FAILOVER_ALLOW_DATA_LOSS`
6. It updates the `SQLServer` CR status with the new primary
7. It patches pod labels (`mssql.popul.io/role`) so Services route to the new primary immediately
8. The `failoverCooldown` prevents flapping (no new auto-failover within the cooldown period)

## Split-brain protection (fencing)

With `CLUSTER_TYPE=NONE`, SQL Server has no built-in mechanism to prevent a former primary from reclaiming its role after restart. The operator handles this automatically.

### What happens

When the operator detects **two replicas both reporting PRIMARY**:

1. Traffic is cut immediately -- the rogue's pod label is set to `secondary`, removing it from the read-write Service
2. The replica with the **highest LSN** (most recent data) is kept as legitimate primary
3. The rogue is demoted via `ALTER AVAILABILITY GROUP ... SET (ROLE = SECONDARY)`
4. If the same replica reclaims PRIMARY again, fencing escalates to `DROP AVAILABILITY GROUP` (hard fencing), and the replica is automatically rejoined on the next cycle

### Monitoring fencing

```bash
# Check fencing status on the AG
kubectl get msag myag -n mssql -o jsonpath='{.status}' | jq '{
  primaryReplica, lastFencedReplica, fencingCount,
  consecutiveFencingCount, lastFencingTime
}'

# Check events
kubectl get events -n mssql --field-selector reason=SplitBrainDetected
kubectl get events -n mssql --field-selector reason=FencingExecuted
```

### Circuit-breaker

After 5 consecutive fencing attempts on the same replica, the operator stops and sets `Ready=False` with `Reason=FencingExhausted`. This requires manual investigation:

```bash
# Check if fencing is exhausted
kubectl get msag myag -n mssql -o jsonpath='{.status.conditions[?(@.reason=="FencingExhausted")]}'
```

Resolve the root cause (e.g., misconfigured replica, network partition), then the operator resumes normal operation on the next reconciliation.

### When fencing does NOT trigger

- **Single primary differs from status** -- this is a stale status (e.g., DBA ran a manual failover). The operator corrects the status without fencing.
- **CLUSTER_TYPE=WSFC or External** -- these have their own cluster manager.
- **AGFailover CR in progress** -- fencing is suspended to avoid conflict.
- **First deployment** -- `status.primaryReplica` is not yet set.

For architecture details, see [Split-brain fencing](../explanation/architecture.md#split-brain-fencing).

## Configuration reference

### Availability Group settings

| Field | Default | Description |
|---|---|---|
| `agName` | `{name}-ag` | AG name on SQL Server |
| `availabilityMode` | `SynchronousCommit` | `SynchronousCommit` or `AsynchronousCommit` |
| `autoFailover` | `true` | Operator-managed automatic failover |
| `healthCheckInterval` | `10s` | How often to check primary health |
| `failoverCooldown` | `60s` | Minimum time between auto-failovers |
| `databases` | `[]` | Databases to include in the AG |

### Certificate settings

| Field | Default | Description |
|---|---|---|
| `mode` | `SelfSigned` | `SelfSigned` or `CertManager` |
| `issuerRef` | none | cert-manager issuer reference (required for CertManager mode) |
| `duration` | `8760h` | Certificate validity duration |
| `renewBefore` | `720h` | Renewal window before expiry |

### Services created (HA mode)

| Service | Name | Description |
|---|---|---|
| Read-Write | `{name}` | Routes to primary only (`mssql.popul.io/role=primary`) |
| Read-Only | `{name}-readonly` | Routes to secondaries only (`mssql.popul.io/role=secondary`) |
| Headless | `{name}-headless` | Pod DNS + HADR communication |

### Pod labels (HA mode)

| Label | Values | Description |
|---|---|---|
| `mssql.popul.io/role` | `primary` / `secondary` | Set by the operator, updated on failover |

### SQLServer status (HA-specific)

| Field | Description |
|---|---|
| `readyReplicas` | Number of ready pods |
| `primaryReplica` | Current primary pod name |
| `certificatesReady` | Whether HADR certificates are provisioned |

## Troubleshooting

### Replicas not becoming ready

```bash
# Check pod status
kubectl get pods -n mssql -l app.kubernetes.io/instance=mssql

# Check pod logs
kubectl logs mssql-0 -n mssql
```

Common causes: insufficient memory (SQL Server needs at least 2Gi), storage provisioning failures.

### Auto-failover not triggering

1. Verify `autoFailover: true` in the spec
2. Check operator logs: `kubectl logs -l app.kubernetes.io/name=mssql-operator -n mssql-operator-system`
3. Verify the operator has RBAC for Leases: `coordination.k8s.io/leases`
4. Check if `failoverCooldown` hasn't expired yet

### Certificates not ready

```bash
# Check certificate secrets
kubectl get secrets -n mssql | grep cert

# Check operator logs for certificate errors
kubectl logs -l app.kubernetes.io/name=mssql-operator -n mssql-operator-system | grep -i cert
```

### Edition limitations

| Edition | AG support |
|---|---|
| Enterprise | Full (unlimited replicas, read-scale) |
| Standard | Basic (2 replicas) |
| Developer | Full (non-production only) |
| Express | Not supported |

See [SQL Server version and edition guide](sql-server-version-edition.md) for details.

## Tuning SQL Server for HA

For production HA clusters, configure `mssql.conf` via the `config` field:

```yaml
spec:
  instance:
    config: |
      [memory]
      memorylimitmb = 6144

      [sqlagent]
      enabled = true

      [traceflag]
      traceflag0 = 1222
    resources:
      limits:
        memory: 8Gi
```

If `memorylimitmb` is omitted, the operator auto-sets it to 80% of `resources.limits.memory`.

See [Configuring SQL Server](deploy-sql-server.md#configuring-sql-server-mssqlconf) for the full option reference.
