# Tutorial: Getting started with the MSSQL Operator

In this tutorial you will install the MSSQL Kubernetes Operator, create a SQL Server database, a login, and a database user, all using declarative Kubernetes manifests. By the end, you will have a fully managed SQL Server setup.

## Prerequisites

You need:

- A running Kubernetes cluster (minikube, kind, or any 1.27+ cluster)
- `kubectl` and `helm` installed
- A SQL Server 2019+ instance accessible from inside the cluster

This tutorial uses `mssql.database.svc.cluster.local` as the SQL Server address. Replace it with your actual address.

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

## Step 2: Create a credentials Secret

The operator needs credentials to connect to SQL Server. Create a Secret:

```bash
kubectl create secret generic mssql-sa-credentials \
  --from-literal=username=sa \
  --from-literal=password='YourStrongPassword!'
```

## Step 3: Create a database

Save this as `database.yaml`:

```yaml
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

## Step 4: Create a login

The login is a SQL Server authentication identity. Save this as `login.yaml`:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Login
metadata:
  name: myapp-login
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
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

## Step 5: Create a database user

The database user maps the login to the database. Save this as `dbuser.yaml`:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: DatabaseUser
metadata:
  name: myapp-dbuser
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
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

## Step 6: Verify on SQL Server

Connect to SQL Server and verify the objects were created:

```bash
kubectl run test-sql --rm -it --image=mcr.microsoft.com/mssql-tools -- \
  /opt/mssql-tools/bin/sqlcmd -S mssql.database.svc.cluster.local -U sa -P 'YourStrongPassword!'
```

```sql
SELECT name FROM sys.databases WHERE name = 'myapp';
SELECT name FROM sys.server_principals WHERE name = 'myapp_user';
USE myapp;
SELECT name FROM sys.database_principals WHERE name = 'myapp_user';
GO
```

## Step 7: Clean up

Delete the resources in reverse order:

```bash
kubectl delete databaseuser myapp-dbuser
kubectl delete login myapp-login
kubectl delete database myapp-db
```

By default, `deletionPolicy` is `Retain` -- the database and login are kept on SQL Server even after the CRs are deleted. To actually drop them, set `deletionPolicy: Delete` in the spec before deleting.

## What you learned

- The operator manages SQL Server objects through Kubernetes Custom Resources
- Each CR reports its state via the `Ready` condition
- Resources reference SQL Server credentials via Kubernetes Secrets
- Deletion is safe by default (`Retain` policy)

## Next steps

- [How to manage schemas and permissions](../how-to/manage-schemas-permissions.md)
- [CRD reference](../reference/crds.md) for all available fields
- [Architecture](../explanation/architecture.md) to understand how the operator works internally
