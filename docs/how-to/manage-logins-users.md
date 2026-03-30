# How to manage logins and users

## Create a login

First, ensure you have a `SQLServer` CR deployed ([Deploy SQL Server](deploy-sql-server.md)).

Create a Secret for the login password:

```bash
kubectl create secret generic myapp-login-password \
  --from-literal=password='AppP@ssw0rd!'
```

Then create the login:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Login
metadata:
  name: myapp-login
spec:
  server:
    sqlServerRef: mssql          # references your SQLServer CR
  loginName: myapp_user
  passwordSecret:
    name: myapp-login-password
```

> You can also specify inline connection details instead of `sqlServerRef`:
> ```yaml
> server:
>   host: mssql.database.svc.cluster.local
>   credentialsSecret:
>     name: mssql-sa-credentials
> ```

## Add server roles

```yaml
spec:
  serverRoles:
    - dbcreator
    - securityadmin
```

Roles are reconciled continuously. Adding or removing roles from the list applies the change on SQL Server.

## Set a default database

```yaml
spec:
  defaultDatabase: myapp
```

## Rotate a password

Update the Secret:

```bash
kubectl create secret generic myapp-login-password \
  --from-literal=password='NewP@ssw0rd!' \
  --dry-run=client -o yaml | kubectl apply -f -
```

The operator watches Secrets and detects the change. It calls `ALTER LOGIN ... WITH PASSWORD` automatically. No change to the Login CR is needed.

## Create a database user

A database user maps a login to a specific database:

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

The `loginRef.name` must reference an existing Login CR in the same namespace. The Login must be `Ready=True` before the user can be created.

## Change database roles

Update the `databaseRoles` list. The operator adds missing roles and removes extra ones.

```yaml
spec:
  databaseRoles:
    - db_datareader
    - db_datawriter
    - db_ddladmin       # added
```

## Delete a login

If the login has dependent database users, deletion is blocked. Delete the `DatabaseUser` CRs first:

```bash
kubectl delete databaseuser myapp-dbuser
kubectl delete login myapp-login
```

With `deletionPolicy: Delete`, the login is dropped on SQL Server. With `Retain` (default), it is kept.

## Delete a database user

```bash
kubectl delete databaseuser myapp-dbuser
```

The user is always dropped from the database on deletion. If the user owns objects (schemas, etc.), deletion is blocked until ownership is transferred.
