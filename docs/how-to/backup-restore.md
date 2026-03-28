# How to back up and restore databases

## Back up a database

Create a `Backup` CR to trigger a one-shot backup of a SQL Server database.

### Full backup

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Backup
metadata:
  name: mydb-backup-full
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: mydb
  destination: /var/opt/mssql/backups/mydb-full.bak
  type: Full
```

### Differential backup

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Backup
metadata:
  name: mydb-backup-diff
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: mydb
  destination: /var/opt/mssql/backups/mydb-diff.bak
  type: Differential
```

### Log backup

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Backup
metadata:
  name: mydb-backup-log
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: mydb
  destination: /var/opt/mssql/backups/mydb-log.trn
  type: Log
```

### With compression

```yaml
spec:
  compression: true
```

### Check backup status

```bash
kubectl get msbak
kubectl describe msbak mydb-backup-full
```

Phases: `Pending` → `Running` → `Completed` or `Failed`.

## Restore a database

Create a `Restore` CR to restore a database from a backup file.

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Restore
metadata:
  name: mydb-restore
spec:
  server:
    host: mssql.database.svc.cluster.local
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: mydb
  source: /var/opt/mssql/backups/mydb-full.bak
```

### Check restore status

```bash
kubectl get msrestore
kubectl describe msrestore mydb-restore
```

## Important notes

- **One-shot operations**: Backup and Restore CRs execute once. Once `Completed` or `Failed`, they are never re-executed.
- **Immutable spec**: The spec cannot be changed after creation. To re-run, delete the CR and create a new one.
- **File paths are on the SQL Server filesystem**: The `destination` and `source` paths refer to the SQL Server container's filesystem, not the Kubernetes node.
- **Restore uses `WITH REPLACE`**: The restore will overwrite an existing database with the same name.
- **No finalizer**: Backup and Restore CRs do not use finalizers — deleting the CR does not affect the backup file or restored database.
