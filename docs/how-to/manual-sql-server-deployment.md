# Deploy SQL Server manually (without the operator)

This guide covers deploying SQL Server in Kubernetes **manually** (Deployment, StatefulSet, Helm, etc.) and registering it as an external instance with the operator.

> **Recommended approach**: Use the [`SQLServer` CR with `spec.instance`](deploy-sql-server.md) to let the operator manage the deployment for you. This guide is for cases where you need full control over the SQL Server infrastructure (custom Helm charts, ArgoCD, Terraform, etc.).

## Standalone instance

### Step 1: Create the namespace and secrets

```bash
kubectl create namespace mssql

kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!' \
  -n mssql

kubectl create secret generic sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStr0ngP@ssword!' \
  -n mssql
```

### Step 2: Deploy SQL Server

Save this as `sql-server.yaml`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: mssql
  namespace: mssql
spec:
  selector:
    app: mssql
  ports:
    - port: 1433
      targetPort: 1433
      name: sql
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mssql
  namespace: mssql
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mssql
  template:
    metadata:
      labels:
        app: mssql
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
          ports:
            - containerPort: 1433
              name: sql
          resources:
            requests:
              memory: "512Mi"
              cpu: "250m"
            limits:
              memory: "2Gi"
          volumeMounts:
            - name: mssql-data
              mountPath: /var/opt/mssql
          readinessProbe:
            tcpSocket:
              port: 1433
            initialDelaySeconds: 20
            periodSeconds: 10
          livenessProbe:
            tcpSocket:
              port: 1433
            initialDelaySeconds: 30
            periodSeconds: 15
      volumes:
        - name: mssql-data
          persistentVolumeClaim:
            claimName: mssql-data
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mssql-data
  namespace: mssql
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
```

```bash
kubectl apply -f sql-server.yaml
kubectl wait --for=condition=available deployment/mssql -n mssql --timeout=120s
```

### Step 3: Register with the operator

Create a `SQLServer` CR in **external mode** (no `spec.instance`):

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
  namespace: mssql
spec:
  host: mssql.mssql.svc.cluster.local
  credentialsSecret:
    name: sa-credentials
```

```bash
kubectl apply -f sqlserver.yaml
kubectl get sqlsrv mssql -n mssql
```

You can now reference this `SQLServer` CR in your other CRs via `sqlServerRef: mssql`.

## Multi-replica HA cluster (manual)

If you need to manage the StatefulSet, certificates, and AG yourself, follow this approach.

### Step 1: Deploy a 2-replica StatefulSet with HADR

```yaml
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

```bash
kubectl apply -f sql-server.yaml
kubectl rollout status statefulset/sql -n mssql --timeout=120s
```

### Step 2: Set up HADR certificates

SQL Server on Linux requires certificate-based authentication for database mirroring endpoints.

```bash
SA_PASSWORD='YourStr0ngP@ssword!'

run_sql() {
  kubectl exec "$1" -n mssql -- /opt/mssql-tools18/bin/sqlcmd \
    -S localhost -U sa -P "$SA_PASSWORD" -Q "$2" -C -No
}

# Create master keys and certificates
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
  kubectl cp "mssql/sql-$i:/var/opt/mssql/backup/ag_cert_$i.cer" "$TMPDIR/ag_cert_$i.cer"
  kubectl cp "mssql/sql-$i:/var/opt/mssql/backup/ag_cert_$i.key" "$TMPDIR/ag_cert_$i.key"
  kubectl cp "$TMPDIR/ag_cert_$i.cer" "mssql/sql-$peer:/var/opt/mssql/backup/ag_cert_$i.cer"
  kubectl cp "$TMPDIR/ag_cert_$i.key" "mssql/sql-$peer:/var/opt/mssql/backup/ag_cert_$i.key"
done

# Import peer certificates, create logins, create endpoints
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

### Step 3: Create the AG with the operator

Once the infrastructure is in place, use the `AvailabilityGroup` CR:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: AvailabilityGroup
metadata:
  name: myapp-ag
  namespace: mssql
spec:
  agName: myag
  clusterType: "None"
  autoFailover: true
  healthCheckInterval: "10s"
  failoverCooldown: "60s"
  replicas:
    - serverName: sql-0
      endpointURL: "TCP://sql-0.sql-headless.mssql.svc.cluster.local:5022"
      availabilityMode: SynchronousCommit
      failoverMode: Manual
      seedingMode: Automatic
      server:
        host: sql-0.sql-headless.mssql.svc.cluster.local
        credentialsSecret:
          name: sa-credentials
    - serverName: sql-1
      endpointURL: "TCP://sql-1.sql-headless.mssql.svc.cluster.local:5022"
      availabilityMode: SynchronousCommit
      failoverMode: Manual
      seedingMode: Automatic
      server:
        host: sql-1.sql-headless.mssql.svc.cluster.local
        credentialsSecret:
          name: sa-credentials
```

### Step 4: Manual failover

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

## When to use manual deployment

| Use case | Recommendation |
|---|---|
| Simple development/test setup | Use the managed `SQLServer` CR |
| Production with custom Helm charts | Manual deployment + external mode |
| SQL Server managed by another team | External mode only |
| Azure SQL / RDS / managed services | External mode only |
| Full control over StatefulSet config | Manual deployment |
| Quick HA cluster setup | Use managed `SQLServer` CR with `replicas: 3` |
