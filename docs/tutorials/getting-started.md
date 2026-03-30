# Tutorial: Getting started with the MSSQL Operator

In this tutorial you will install the MSSQL Kubernetes Operator, deploy a SQL Server instance via the `SQLServer` CR, then create a database, a login, and a database user -- all declaratively. By the end, you will have a fully managed SQL Server setup running entirely in Kubernetes.

## Prerequisites

You need:

- A running Kubernetes cluster (minikube, kind, or any 1.27+ cluster)
- `kubectl` and `helm` installed

No external SQL Server is needed -- the operator deploys one for you.

## Step 1: Install the operator

```bash
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace
```

Wait for the operator pod to be ready:

```bash
kubectl get pods -n mssql-operator-system -w
```

You should see:

```
NAME                              READY   STATUS    RESTARTS   AGE
mssql-operator-6d4f8b7c9f-x2k4l  1/1     Running   0          30s
```

## Step 2: Create Secret

```bash
# SA password — used by both the SQL Server container and the operator
kubectl create secret generic mssql-sa-password \
  --from-literal=MSSQL_SA_PASSWORD='YourStr0ngP@ssword!'
```

## Step 3: Deploy SQL Server with the SQLServer CR

Save this as `sqlserver.yaml`:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
spec:
  instance:
    acceptEULA: true
    saPasswordSecret:
      name: mssql-sa-password
    storageSize: 10Gi
    resources:
      requests:
        memory: 2Gi
        cpu: 500m
      limits:
        memory: 4Gi
```

Apply it:

```bash
kubectl apply -f sqlserver.yaml
```

The operator creates a StatefulSet, Services, and PVCs automatically. Watch it become ready:

```bash
kubectl get sqlsrv mssql -w
```

```
NAME    HOST                                PORT   AUTH       READY   VERSION          AGE
mssql   mssql.default.svc.cluster.local     1433   SqlLogin   True    16.0.4135.4      45s
```

## Step 4: Create a database

Save this as `database.yaml`:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
spec:
  server:
    sqlServerRef: mssql
  databaseName: myapp
```

Apply it:

```bash
kubectl apply -f database.yaml
```

Check that it becomes ready:

```bash
kubectl get msdb myapp-db
```

```
NAME       DATABASE   READY   AGE
myapp-db   myapp      True    10s
```

## Step 5: Create a login

The login is a SQL Server authentication identity. Save this as `login.yaml`:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Login
metadata:
  name: myapp-login
spec:
  server:
    sqlServerRef: mssql
  loginName: myapp_user
  passwordSecret:
    name: myapp-login-password
```

Create the password Secret and the login:

```bash
kubectl create secret generic myapp-login-password \
  --from-literal=password='AppP@ssw0rd!'

kubectl apply -f login.yaml
```

Check:

```bash
kubectl get mslogin myapp-login
```

```
NAME           LOGIN        READY   AGE
myapp-login    myapp_user   True    10s
```

## Step 6: Create a database user

The database user maps the login to the database. Save this as `dbuser.yaml`:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: DatabaseUser
metadata:
  name: myapp-dbuser
spec:
  server:
    sqlServerRef: mssql
  databaseName: myapp
  userName: myapp_user
  loginRef:
    name: myapp-login
  databaseRoles:
    - db_datareader
    - db_datawriter
```

Apply it:

```bash
kubectl apply -f dbuser.yaml
```

Check:

```bash
kubectl get msuser myapp-dbuser
```

```
NAME           DATABASE   USER         READY   AGE
myapp-dbuser   myapp      myapp_user   True    10s
```

## Step 7: Verify on SQL Server

Connect to SQL Server and verify the objects were created:

```bash
kubectl exec mssql-0 -- /opt/mssql-tools18/bin/sqlcmd \
  -S localhost -U sa -P 'YourStr0ngP@ssword!' -C -No \
  -Q "SELECT name FROM sys.databases WHERE name = 'myapp'; SELECT name FROM sys.server_principals WHERE name = 'myapp_user'"
```

## Step 8: Clean up

Delete the resources in reverse order:

```bash
kubectl delete databaseuser myapp-dbuser
kubectl delete login myapp-login
kubectl delete database myapp-db
kubectl delete sqlserver mssql
```

By default, `deletionPolicy` is `Retain` -- the database and login are kept on SQL Server even after the CRs are deleted. To actually drop them, set `deletionPolicy: Delete` in the spec before deleting. When the `SQLServer` CR is deleted, the StatefulSet, Services, and PVCs are garbage-collected automatically.

## What you learned

- The operator can **deploy SQL Server** via the `SQLServer` CR (no manual Deployment/StatefulSet needed)
- Other CRs reference the SQL Server by name via `sqlServerRef`
- Each CR reports its state via the `Ready` condition
- Resources reference credentials via Kubernetes Secrets
- Deletion is safe by default (`Retain` policy)

## Next steps

- [Deploy a HA cluster](../how-to/high-availability.md) with 3 replicas and auto-failover
- [How to manage schemas and permissions](../how-to/manage-schemas-permissions.md)
- [CRD reference](../reference/crds.md) for all available fields
- [Architecture](../explanation/architecture.md) to understand how the operator works internally
