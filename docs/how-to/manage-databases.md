# How to manage databases

## Create a database

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

```bash
kubectl apply -f database.yaml
kubectl get msdb myapp-db
```

## Set a collation

```yaml
spec:
  databaseName: myapp
  collation: SQL_Latin1_General_CP1_CI_AS
```

Collation is **immutable after creation**. This is a SQL Server limitation -- it cannot be changed without recreating the database. The operator rejects collation changes via the admission webhook.

## Set a database owner

```yaml
spec:
  databaseName: myapp
  owner: myapp_user
```

The owner can be changed at any time. The operator runs `ALTER AUTHORIZATION ON DATABASE` to apply the change.

## Delete a database

By default, deleting the CR **retains** the database on SQL Server:

```bash
kubectl delete database myapp-db
# The database still exists on SQL Server
```

To actually drop the database, set `deletionPolicy: Delete`:

```yaml
spec:
  deletionPolicy: Delete
```

Then delete the CR:

```bash
kubectl delete database myapp-db
# The database is DROPped on SQL Server
```

## Point to a different SQL Server

Each CR has its own `server` block. You can manage databases on multiple SQL Server instances from the same cluster:

```yaml
spec:
  server:
    host: production-sql.database.svc.cluster.local
    port: 1433
    tls: true
    credentialsSecret:
      name: production-sa-credentials
```

## Check database status

```bash
# Quick view
kubectl get msdb

# Detailed status
kubectl describe database myapp-db

# JSON path
kubectl get database myapp-db -o jsonpath='{.status.conditions[0].reason}'
```
