# How to manage schemas and permissions

## Create a schema

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Schema
metadata:
  name: app-schema
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
  schemaName: app
  owner: myapp_user           # optional
```

```bash
kubectl apply -f schema.yaml
kubectl get msschema
```

## Change schema owner

Update the `owner` field:

```yaml
spec:
  owner: new_owner
```

The operator runs `ALTER AUTHORIZATION ON SCHEMA::[app] TO [new_owner]`.

## Delete a schema

With `deletionPolicy: Retain` (default), the schema is kept on SQL Server:

```bash
kubectl delete schema app-schema
```

With `deletionPolicy: Delete`, the schema is dropped. If the schema contains objects (tables, views, etc.), deletion is blocked until the objects are moved or dropped. The operator requeues periodically and retries.

## Grant permissions

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Permission
metadata:
  name: app-permissions
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
  userName: myapp_user
  grants:
    - permission: SELECT
      on: "SCHEMA::app"
    - permission: INSERT
      on: "SCHEMA::app"
    - permission: EXECUTE
      on: "SCHEMA::app"
```

```bash
kubectl apply -f permissions.yaml
kubectl get msperm
```

## Deny permissions

```yaml
spec:
  grants:
    - permission: SELECT
      on: "SCHEMA::app"
  denies:
    - permission: DELETE
      on: "SCHEMA::app"
```

## Modify permissions

Update the `grants` and `denies` lists. The operator diffs the desired state against SQL Server and applies the minimum changes:

- New entries are GRANTed or DENYed
- Removed entries are REVOKEd
- Unchanged entries are left as-is

## Supported target formats

| Format | Example | SQL generated |
|--------|---------|---------------|
| `SCHEMA::name` | `SCHEMA::app` | `ON SCHEMA::[app]` |
| `OBJECT::schema.name` | `OBJECT::dbo.Users` | `ON OBJECT::[dbo].[Users]` |
| `DATABASE::name` | `DATABASE::myapp` | `ON DATABASE::[myapp]` |

## Supported permissions

`SELECT`, `INSERT`, `UPDATE`, `DELETE`, `EXECUTE`, `ALTER`, `CONTROL`, `REFERENCES`, `VIEW DEFINITION`, `CREATE TABLE`, `CREATE VIEW`, `CREATE PROCEDURE`, `CREATE FUNCTION`, `CREATE SCHEMA`.

## Delete permissions

When the Permission CR is deleted, the operator REVOKEs all grants and denies listed in the spec. There is no `deletionPolicy` -- permissions are always cleaned up.

```bash
kubectl delete permission app-permissions
```

## Full example: schema + permissions

```yaml
---
apiVersion: mssql.popul.io/v1alpha1
kind: Schema
metadata:
  name: app-schema
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
  schemaName: app
  owner: myapp_user
  deletionPolicy: Delete
---
apiVersion: mssql.popul.io/v1alpha1
kind: Permission
metadata:
  name: app-permissions
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
  userName: myapp_user
  grants:
    - permission: SELECT
      on: "SCHEMA::app"
    - permission: INSERT
      on: "SCHEMA::app"
    - permission: UPDATE
      on: "SCHEMA::app"
    - permission: EXECUTE
      on: "SCHEMA::app"
  denies:
    - permission: DELETE
      on: "SCHEMA::app"
```
