# Change SQL Server version or edition

## With the SQLServer CR (managed mode)

### Change the version

Update the `image` field in your `SQLServer` CR:

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: SQLServer
metadata:
  name: mssql
spec:
  credentialsSecret:
    name: sa-credentials
  instance:
    acceptEULA: true
    image: mcr.microsoft.com/mssql/server:2022-CU16-ubuntu-22.04
    # ...
```

Common tags:

| Tag | Description |
|---|---|
| `2022-latest` | SQL Server 2022, latest cumulative update |
| `2019-latest` | SQL Server 2019, latest cumulative update |
| `2022-CU16-ubuntu-22.04` | SQL Server 2022, CU16 specifically |

The operator performs a rolling update of the StatefulSet. If you have an Availability Group with `autoFailover: true`, the primary switchover is handled automatically.

### Change the edition

Update the `edition` field:

```yaml
instance:
  edition: Enterprise    # Developer, Express, Standard, Enterprise, EnterpriseCore
```

| Edition | Licence | AG support | Limites |
|---|---|---|---|
| `Developer` | Gratuit (non-prod) | Oui | Pas de production |
| `Express` | Gratuit | Non | 10 Go par base, 1 Go RAM |
| `Standard` | Payant | Basique (2 replicas) | Pas de read-scale |
| `Enterprise` | Payant | Complet | Aucune |
| `EnterpriseCore` | Payant (par core) | Complet | Aucune |

Par defaut, l'edition est **Developer**.

> **Note**: Express edition ne supporte pas les Availability Groups. Le webhook de validation bloque la creation d'un `SQLServer` CR avec `edition: Express` et `replicas > 1`.

### Apply and verify

```bash
kubectl apply -f sqlserver.yaml

# Watch the rolling update
kubectl get pods -n mssql -w

# Check the version in the status
kubectl get sqlsrv mssql -n mssql -o jsonpath='{.status.serverVersion} {.status.edition}'
```

## With a manual deployment

If you manage SQL Server yourself (see [Manual deployment](manual-sql-server-deployment.md)), the version and edition are controlled by the Docker image and the `MSSQL_PID` environment variable.

### Change the version

Update the `image` in your StatefulSet or Deployment:

```yaml
containers:
  - name: mssql
    image: mcr.microsoft.com/mssql/server:2022-CU16-ubuntu-22.04
```

### Change the edition

Set `MSSQL_PID` in the environment variables:

```yaml
containers:
  - name: mssql
    env:
      - name: MSSQL_PID
        value: "Enterprise"
```

Apply and restart:

```bash
kubectl apply -f sql-server.yaml
kubectl rollout restart statefulset/sql -n mssql
kubectl rollout status statefulset/sql -n mssql --timeout=120s
```

## Verify the version and edition

Via `sqlcmd`:

```bash
kubectl exec mssql-0 -n mssql -- /opt/mssql-tools18/bin/sqlcmd \
  -S localhost -U sa -P "$SA_PASSWORD" \
  -Q "SELECT SERVERPROPERTY('ProductVersion'), SERVERPROPERTY('Edition')" -C -No
```

Or via the `SQLServer` CR status:

```bash
kubectl get sqlsrv mssql -n mssql -o jsonpath='{.status.serverVersion} {.status.edition}'
```
