# Deploy SQL Server in Kubernetes

This guide shows how to deploy a single SQL Server instance in Kubernetes. This is the simplest setup and a prerequisite for using the mssql-k8s-operator.

> For a multi-replica Availability Group setup, see [High Availability](high-availability.md).

## Architecture

```
    Kubernetes Cluster
    +-------------------------------------------+
    |                                           |
    |   +-------------------+                   |
    |   |  mssql (Pod)      |                   |
    |   |  SQL Server 2022  |                   |
    |   |  port 1433        |                   |
    |   +-------------------+                   |
    |           |                               |
    |   +-------------------+                   |
    |   | mssql-svc (Svc)   |                   |
    |   | ClusterIP :1433   |                   |
    |   +-------------------+                   |
    |           |                               |
    |   +-------------------+                   |
    |   | mssql-data (PVC)  |                   |
    |   | 10Gi              |                   |
    |   +-------------------+                   |
    +-------------------------------------------+
```

## Prerequisites

- A Kubernetes cluster (minikube, kind, k3d, EKS, GKE, AKS...)
- `kubectl` installed
- A StorageClass available for persistent volumes (most clusters have a default)

## Step 1: Create the namespace and SA password

```bash
kubectl create namespace mssql

# SA password must meet SQL Server complexity requirements:
# at least 8 characters, with uppercase, lowercase, digit, and special character
kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!' \
  -n mssql
```

## Step 2: Deploy SQL Server

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

Apply and wait for the pod to be ready:

```bash
kubectl apply -f sql-server.yaml
kubectl wait --for=condition=available deployment/mssql -n mssql --timeout=120s
```

Verify:

```bash
kubectl get pods -n mssql
# NAME                     READY   STATUS    RESTARTS   AGE
# mssql-7b8f6d9c4f-x2k4l  1/1     Running   0          45s
```

## Step 3: Create the operator credentials Secret

The operator uses a separate Secret with `username` and `password` keys:

```bash
kubectl create secret generic sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStr0ngP@ssword!' \
  -n mssql
```

## Step 4: Register the server with the operator (optional)

You can create a `SQLServer` CR to let the operator monitor the connection and expose version/edition info:

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

## Step 5: Verify the connection

```bash
kubectl run test-sql --rm -it --restart=Never -n mssql \
  --image=mcr.microsoft.com/mssql-tools18 -- \
  /opt/mssql-tools18/bin/sqlcmd \
    -S mssql.mssql.svc.cluster.local \
    -U sa -P 'YourStr0ngP@ssword!' \
    -Q "SELECT @@VERSION" -C -No
```

## Using it with the operator

Your SQL Server is now reachable at `mssql.mssql.svc.cluster.local:1433`. Use this address in your CRs:

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
| **Edition** | Set `MSSQL_PID` for production editions. See [version and edition guide](sql-server-version-edition.md) |
| **TLS** | Enable TLS for production. Mount certificates and set `MSSQL_TLS_CERT` / `MSSQL_TLS_KEY` env vars |
| **Backups** | Use the operator's `ScheduledBackup` CR or mount additional PVCs for backup storage |
| **High availability** | For HA, use a StatefulSet with 2+ replicas and an Availability Group. See [HA guide](high-availability.md) |

## Exposing SQL Server outside the cluster

To access SQL Server from outside the cluster, change the Service type:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: mssql-external
  namespace: mssql
spec:
  type: LoadBalancer    # or NodePort
  selector:
    app: mssql
  ports:
    - port: 1433
      targetPort: 1433
```

> **Warning**: never expose SQL Server to the public internet without TLS and strong credentials.
