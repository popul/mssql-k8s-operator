# Deploy SQL Server in Kubernetes

This guide shows how to deploy SQL Server in Kubernetes. You can either let the operator manage the deployment automatically, or create the resources yourself.

> For a multi-replica Availability Group setup, see [High Availability](high-availability.md).

## Option A: Operator-managed deployment (recommended)

The operator can create and manage the StatefulSet, Services, and PVCs for you via the `SQLServer` CR.

### Step 1: Create Secrets

```bash
kubectl create namespace mssql

# SA password for the SQL Server container
kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!' \
  -n mssql

# Operator credentials (must use the same password)
kubectl create secret generic sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStr0ngP@ssword!' \
  -n mssql
```

### Step 2: Create the SQLServer CR

**Standalone instance (1 replica):**

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
  namespace: mssql
spec:
  credentialsSecret:
    name: sa-credentials
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

**HA cluster (3 replicas with Availability Group):**

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
  namespace: mssql
spec:
  credentialsSecret:
    name: sa-credentials
  instance:
    acceptEULA: true
    image: mcr.microsoft.com/mssql/server:2022-latest
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
      mode: SelfSigned        # or CertManager
    availabilityGroup:
      agName: myag
      availabilityMode: SynchronousCommit
      autoFailover: true
```

### Step 3: Apply and verify

```bash
kubectl apply -f sqlserver.yaml

# Watch the status
kubectl get sqlsrv mssql -n mssql -w
```

The operator will create:
- A **StatefulSet** with the requested replicas
- A **headless Service** (`mssql-headless`) for inter-pod DNS
- A **client Service** (`mssql`) for application access
- **PVCs** per replica via VolumeClaimTemplates
- **HADR certificates** (if replicas > 1)
- An **Availability Group** (if replicas > 1)

### Instance spec reference

| Field | Default | Description |
|---|---|---|
| `acceptEULA` | required | Must be `true` |
| `image` | `mssql/server:2022-latest` | SQL Server container image |
| `edition` | `Developer` | `Developer`, `Express`, `Standard`, `Enterprise` |
| `replicas` | `1` | 1 = standalone, 2-5 = AG cluster |
| `saPasswordSecret` | required | Secret with `MSSQL_SA_PASSWORD` key |
| `storageSize` | `10Gi` | PVC size per replica |
| `storageClassName` | cluster default | StorageClass (immutable) |
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

---

## Option B: Manual deployment

If you prefer to manage the SQL Server deployment yourself (e.g. via Helm, ArgoCD, or Terraform), you can deploy it manually and point the operator to it.

### Step 1: Create the namespace and SA password

```bash
kubectl create namespace mssql

kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!' \
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

### Step 3: Create the operator credentials Secret

```bash
kubectl create secret generic sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStr0ngP@ssword!' \
  -n mssql
```

### Step 4: Register with the operator (external mode)

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

---

## Using it with the operator

Once your SQL Server is running (either managed or external), reference it in your CRs:

```yaml
spec:
  server:
    sqlServerRef: mssql    # references the SQLServer CR by name
  databaseName: myapp
```

Or use inline connection details:

```yaml
spec:
  server:
    host: mssql.mssql.svc.cluster.local
    credentialsSecret:
      name: sa-credentials
  databaseName: myapp
```

## Production considerations

| Topic | Recommendation |
|---|---|
| **Persistence** | Always use a PVC. Without it, data is lost on pod restart |
| **Resources** | Set memory limits based on your workload. SQL Server uses all available memory by default |
| **Edition** | Set `edition` (managed) or `MSSQL_PID` (manual) for production. See [version and edition guide](sql-server-version-edition.md) |
| **TLS** | Enable TLS for production |
| **Backups** | Use the operator's `ScheduledBackup` CR |
| **High availability** | Use `replicas: 3` in managed mode, or see [HA guide](high-availability.md) for manual setup |

## Exposing SQL Server outside the cluster

In managed mode, set `serviceType: LoadBalancer`:

```yaml
instance:
  serviceType: LoadBalancer
```

In manual mode, change the Service type:

```yaml
spec:
  type: LoadBalancer
  selector:
    app: mssql
  ports:
    - port: 1433
      targetPort: 1433
```

> **Warning**: never expose SQL Server to the public internet without TLS and strong credentials.
