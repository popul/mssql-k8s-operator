# Set up a SQL Server HA cluster from scratch

This guide walks you through deploying a 2-node SQL Server Availability Group with automatic failover in Kubernetes, from zero to a working HA cluster.

## Architecture

```
                    Kubernetes Cluster
    +-------------------------------------------------+
    |                                                 |
    |   Operator (mssql-operator)                     |
    |   - Monitors replicas every 5s                  |
    |   - Auto-failover on primary failure            |
    |   - Lease-based split-brain prevention           |
    |                                                 |
    |   +-------------+    HADR    +-------------+    |
    |   |   sql-0     |<-- TCP -->|   sql-1     |    |
    |   |  PRIMARY    |   5022    | SECONDARY   |    |
    |   |  port 1433  |           |  port 1433  |    |
    |   +-------------+           +-------------+    |
    |         |                         |             |
    |   +-----+-----------+-------------+-----+       |
    |   |       sql-headless (Headless Svc)    |       |
    |   +--------------------------------------+       |
    +-------------------------------------------------+
```

## Prerequisites

- A Kubernetes cluster (kind, k3d, EKS, GKE, AKS...)
- `kubectl` and `helm` installed
- The mssql-k8s-operator installed with CRDs

> For a single-instance SQL Server setup, see [Deploy SQL Server in Kubernetes](deploy-sql-server.md). This guide deploys a multi-replica StatefulSet for HA.

## Step 1: Create the namespace and secrets

```bash
kubectl create namespace mssql

# SA password for SQL Server (must meet complexity requirements)
kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!' \
  -n mssql

# Credentials secret for the operator
kubectl create secret generic sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStr0ngP@ssword!' \
  -n mssql
```

## Step 2: Deploy 2 SQL Server instances with HADR

Create a headless Service for inter-pod DNS resolution and a StatefulSet with 2 replicas:

```yaml
# sql-server.yaml
apiVersion: v1
kind: Service
metadata:
  name: sql-headless
  namespace: mssql
spec:
  clusterIP: None
  selector:
    app: mssql-ag
  ports:
    - { port: 1433, name: sql }
    - { port: 5022, name: hadr }
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: sql
  namespace: mssql
spec:
  serviceName: sql-headless
  replicas: 2
  selector:
    matchLabels:
      app: mssql-ag
  template:
    metadata:
      labels:
        app: mssql-ag
    spec:
      containers:
        - name: mssql
          image: mcr.microsoft.com/mssql/server:2022-latest
          env:
            - name: ACCEPT_EULA
              value: "Y"
            - name: MSSQL_SA_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: mssql-sa-password
                  key: MSSQL_SA_PASSWORD
            - name: MSSQL_ENABLE_HADR
              value: "1"
          ports:
            - { containerPort: 1433, name: sql }
            - { containerPort: 5022, name: hadr }
          resources:
            requests: { memory: "512Mi", cpu: "250m" }
            limits:   { memory: "2Gi" }
          readinessProbe:
            tcpSocket: { port: 1433 }
            initialDelaySeconds: 20
            periodSeconds: 10
```

Apply and wait:

```bash
kubectl apply -f sql-server.yaml
kubectl rollout status statefulset/sql -n mssql --timeout=120s
```

Verify both pods are ready:

```bash
kubectl get pods -n mssql -l app=mssql-ag
# NAME    READY   STATUS    RESTARTS   AGE
# sql-0   1/1     Running   0          60s
# sql-1   1/1     Running   0          45s
```

## Step 3: Set up HADR endpoint certificates

SQL Server on Linux requires certificate-based authentication for database mirroring endpoints. Each instance needs a certificate, and they must exchange them.

```bash
SA_PASSWORD='YourStr0ngP@ssword!'

# Helper to run SQL on a pod
run_sql() {
  kubectl exec "$1" -n mssql -- /opt/mssql-tools18/bin/sqlcmd \
    -S localhost -U sa -P "$SA_PASSWORD" -Q "$2" -C -No
}

# Create master keys and certificates on each pod
for i in 0 1; do
  run_sql "sql-$i" "
    IF NOT EXISTS (SELECT 1 FROM sys.symmetric_keys WHERE name = '##MS_DatabaseMasterKey##')
      CREATE MASTER KEY ENCRYPTION BY PASSWORD = 'MasterKeyP@ss1!';
    IF NOT EXISTS (SELECT 1 FROM sys.certificates WHERE name = 'ag_cert_$i')
      CREATE CERTIFICATE ag_cert_$i WITH SUBJECT = 'sql-$i cert', EXPIRY_DATE = '2030-01-01';
    BACKUP CERTIFICATE ag_cert_$i
      TO FILE = '/var/opt/mssql/backup/ag_cert_$i.cer'
      WITH PRIVATE KEY (
        FILE = '/var/opt/mssql/backup/ag_cert_$i.key',
        ENCRYPTION BY PASSWORD = 'CertP@ss123!'
      );
  "
done

# Exchange certificates between pods
TMPDIR=$(mktemp -d)
for i in 0 1; do
  peer=$((1 - i))
  # Copy cert from sql-$i to local
  kubectl cp "mssql/sql-$i:/var/opt/mssql/backup/ag_cert_$i.cer" "$TMPDIR/ag_cert_$i.cer"
  kubectl cp "mssql/sql-$i:/var/opt/mssql/backup/ag_cert_$i.key" "$TMPDIR/ag_cert_$i.key"
  # Copy cert to peer
  kubectl cp "$TMPDIR/ag_cert_$i.cer" "mssql/sql-$peer:/var/opt/mssql/backup/ag_cert_$i.cer"
  kubectl cp "$TMPDIR/ag_cert_$i.key" "mssql/sql-$peer:/var/opt/mssql/backup/ag_cert_$i.key"
done

# Import peer certificates and create endpoints
for i in 0 1; do
  peer=$((1 - i))
  run_sql "sql-$i" "
    IF NOT EXISTS (SELECT 1 FROM sys.certificates WHERE name = 'ag_cert_$peer')
      CREATE CERTIFICATE ag_cert_$peer
        FROM FILE = '/var/opt/mssql/backup/ag_cert_$peer.cer'
        WITH PRIVATE KEY (
          FILE = '/var/opt/mssql/backup/ag_cert_$peer.key',
          DECRYPTION BY PASSWORD = 'CertP@ss123!'
        );
    IF NOT EXISTS (SELECT 1 FROM sys.server_principals WHERE name = 'ag_login_$peer')
      CREATE LOGIN ag_login_$peer FROM CERTIFICATE ag_cert_$peer;
    IF NOT EXISTS (SELECT 1 FROM sys.database_mirroring_endpoints)
      CREATE ENDPOINT hadr_endpoint
        STATE = STARTED
        AS TCP (LISTENER_PORT = 5022)
        FOR DATABASE_MIRRORING (
          ROLE = ALL,
          AUTHENTICATION = CERTIFICATE ag_cert_$i,
          ENCRYPTION = DISABLED
        );
    GRANT CONNECT ON ENDPOINT::hadr_endpoint TO ag_login_$peer;
  "
done
rm -rf "$TMPDIR"
```

Verify endpoints are running:

```bash
run_sql sql-0 "SELECT name, state_desc FROM sys.database_mirroring_endpoints"
# hadr_endpoint    STARTED
```

## Step 4: Create the Availability Group with auto-failover

```yaml
# ag.yaml
apiVersion: mssql.popul.io/v1alpha1
kind: AvailabilityGroup
metadata:
  name: myapp-ag
  namespace: mssql
spec:
  agName: myag
  clusterType: "None"            # No Pacemaker needed
  autoFailover: true             # Operator-managed automatic failover
  healthCheckInterval: "10s"     # Check every 10 seconds
  failoverCooldown: "60s"        # Wait 60s between auto-failovers

  replicas:
    - serverName: sql-0
      endpointURL: "TCP://sql-0.sql-headless.mssql.svc.cluster.local:5022"
      availabilityMode: SynchronousCommit
      failoverMode: Manual       # Required for CLUSTER_TYPE=NONE
      seedingMode: Automatic
      server:
        host: sql-0.sql-headless.mssql.svc.cluster.local
        port: 1433
        credentialsSecret:
          name: sa-credentials
    - serverName: sql-1
      endpointURL: "TCP://sql-1.sql-headless.mssql.svc.cluster.local:5022"
      availabilityMode: SynchronousCommit
      failoverMode: Manual
      seedingMode: Automatic
      server:
        host: sql-1.sql-headless.mssql.svc.cluster.local
        port: 1433
        credentialsSecret:
          name: sa-credentials

  automatedBackupPreference: Secondary
  dbFailover: false
```

```bash
kubectl apply -f ag.yaml
```

## Step 5: Verify the AG is ready

```bash
# Watch the AG status
kubectl get msag -n mssql -w

# Detailed status
kubectl describe msag myapp-ag -n mssql
```

You should see:

```
Status:
  Primary Replica:  sql-0
  Replicas:
    Server Name:           sql-0
    Role:                  PRIMARY
    Synchronization State: NOT_HEALTHY
    Connected:             true
    Server Name:           sql-1
    Role:                  SECONDARY
    Synchronization State: NOT_HEALTHY
    Connected:             true
```

With `CLUSTER_TYPE=NONE` and no databases, the synchronization state shows `NOT_HEALTHY` -- this is normal. Add databases to get a fully synchronized AG.

## Step 6: Test automatic failover

Simulate a primary failure by stopping SQL Server on sql-0:

```bash
# Stop SQL Server on the primary
kubectl exec sql-0 -n mssql -- /opt/mssql-tools18/bin/sqlcmd \
  -S localhost -U sa -P 'YourStr0ngP@ssword!' \
  -Q "SHUTDOWN WITH NOWAIT" -C -No

# Watch the AG status -- the operator detects the failure and fails over
kubectl get msag myapp-ag -n mssql -w
```

Within 10-30 seconds, you should see:

```
Events:
  Type     Reason                  Message
  ----     ------                  -------
  Warning  ConnectionFailed        failed to connect to primary replica
  Normal   AutoFailoverCompleted   AG myag auto-failover to sql-1 completed
```

Check the status:

```bash
kubectl describe msag myapp-ag -n mssql
```

```
Status:
  Primary Replica:         sql-1          # <-- new primary
  Auto Failover Count:     1
  Last Auto Failover Time: 2026-03-29T...
```

When sql-0 restarts (automatic via the StatefulSet), it rejoins as SECONDARY.

## How auto-failover works

1. The operator pings the primary every `healthCheckInterval` (default: 10s)
2. If the ping fails, it enters the auto-failover path
3. It acquires a **Kubernetes Lease** (`ag-failover-<agName>`) to prevent split-brain if multiple operator replicas are running
4. It connects to each secondary and checks its role
5. If a secondary is reachable, it executes `ALTER AVAILABILITY GROUP ... FORCE_FAILOVER_ALLOW_DATA_LOSS`
6. It updates the AG CR status with the new primary
7. The `failoverCooldown` prevents flapping (no new auto-failover for 60s)

## Manual failover

For planned maintenance, use the `AGFailover` CR:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: AGFailover
metadata:
  name: failover-to-sql0
  namespace: mssql
spec:
  agName: myag
  targetReplica: sql-0
  force: true
  server:
    host: sql-0.sql-headless.mssql.svc.cluster.local
    credentialsSecret:
      name: sa-credentials
```

```bash
kubectl apply -f failover.yaml
kubectl get msagfo failover-to-sql0 -n mssql
# NAME               PHASE       AGE
# failover-to-sql0   Completed   5s
```

## Configuration reference

### AvailabilityGroup spec

| Field | Default | Description |
|---|---|---|
| `clusterType` | `External` | `WSFC`, `External` (Pacemaker), or `None` (operator-managed) |
| `autoFailover` | `false` | Enable operator-managed automatic failover |
| `healthCheckInterval` | `10s` | How often to check primary health |
| `failoverCooldown` | `60s` | Minimum time between auto-failovers |
| `automatedBackupPreference` | `Secondary` | Where automated backups run |
| `dbFailover` | `true` | Database-level health detection |

### AvailabilityGroup status

| Field | Description |
|---|---|
| `primaryReplica` | Current primary server name |
| `replicas[].role` | `PRIMARY`, `SECONDARY`, or `RESOLVING` |
| `replicas[].synchronizationState` | `SYNCHRONIZED`, `SYNCHRONIZING`, or `NOT_SYNCHRONIZING` |
| `replicas[].connected` | Whether the replica is connected |
| `autoFailoverCount` | Total number of automatic failovers |
| `lastAutoFailoverTime` | Timestamp of the last auto-failover |

## Troubleshooting

### AG created but replicas show NOT_HEALTHY

This is normal with `CLUSTER_TYPE=NONE` and no databases. Add databases to the AG to start synchronization.

### Auto-failover not triggering

1. Check `autoFailover: true` is set in the spec
2. Check operator logs: `kubectl logs -l app.kubernetes.io/name=mssql-operator -n mssql-operator-system`
3. Verify the operator has RBAC for Leases: `coordination.k8s.io/leases`
4. Check `failoverCooldown` hasn't expired yet

### Certificate errors on endpoint

Each SQL Server instance needs the peer's certificate imported. Re-run the certificate exchange script from Step 3.

### sql-0 won't rejoin after failover

With `CLUSTER_TYPE=NONE`, SQL Server should automatically rejoin as SECONDARY on restart. If it doesn't, check that HADR is still enabled and the endpoint is started:

```bash
run_sql sql-0 "SELECT name, state_desc FROM sys.database_mirroring_endpoints"
```
