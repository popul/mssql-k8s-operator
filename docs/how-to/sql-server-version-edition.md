# Change SQL Server version or edition

The SQL Server version and edition are controlled by the **Docker image** and the `MSSQL_PID` environment variable in the StatefulSet (or Deployment) that runs SQL Server.

## Change the version

Update the `image` field in your StatefulSet or Deployment:

```yaml
containers:
  - name: mssql
    image: mcr.microsoft.com/mssql/server:2022-CU16-ubuntu-22.04  # specific CU
```

Common tags:

| Tag | Description |
|---|---|
| `2022-latest` | SQL Server 2022, latest cumulative update |
| `2019-latest` | SQL Server 2019, latest cumulative update |
| `2022-CU16-ubuntu-22.04` | SQL Server 2022, CU16 specifically |

## Change the edition

Add or update `MSSQL_PID` in the environment variables:

```yaml
containers:
  - name: mssql
    image: mcr.microsoft.com/mssql/server:2022-latest
    env:
      - name: MSSQL_PID
        value: "Enterprise"    # or Developer, Express, Standard, EnterpriseCore
      - name: ACCEPT_EULA
        value: "Y"
      - name: MSSQL_SA_PASSWORD
        valueFrom:
          secretKeyRef:
            name: mssql-sa-password
            key: MSSQL_SA_PASSWORD
```

| MSSQL_PID | Licence | AG support | Limites |
|---|---|---|---|
| `Developer` | Gratuit (non-prod) | Oui | Pas de production |
| `Express` | Gratuit | Non | 10 Go par base, 1 Go RAM |
| `Standard` | Payant | Basique (2 replicas) | Pas de read-scale |
| `Enterprise` | Payant | Complet | Aucune |
| `EnterpriseCore` | Payant (par core) | Complet | Aucune |

Par defaut (sans `MSSQL_PID`), l'image utilise **Developer Edition**.

## Apply the change

```bash
kubectl apply -f sql-server.yaml
kubectl rollout restart statefulset/sql -n mssql
kubectl rollout status statefulset/sql -n mssql --timeout=120s
```

If you use an Availability Group with `autoFailover: true`, the operator handles the primary switchover automatically during the rolling update.

## Verify the version and edition

Via `sqlcmd`:

```bash
kubectl exec sql-0 -n mssql -- /opt/mssql-tools18/bin/sqlcmd \
  -S localhost -U sa -P "$SA_PASSWORD" \
  -Q "SELECT SERVERPROPERTY('ProductVersion'), SERVERPROPERTY('Edition')" -C -No
```

Or via the `SQLServer` CRD if you have one:

```bash
kubectl get sqlserver -n mssql -o jsonpath='{.items[0].status.serverVersion} {.items[0].status.edition}'
```
