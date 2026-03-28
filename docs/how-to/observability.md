# Observability Guide

## Metrics

The operator exposes Prometheus metrics on `:8080/metrics`.

### Operator Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `mssql_operator_reconcile_total` | Counter | Total reconciliation attempts by controller and result |
| `mssql_operator_reconcile_errors_total` | Counter | Reconciliation errors by controller and reason |
| `mssql_operator_reconcile_duration_seconds` | Histogram | Reconciliation duration in seconds |
| `mssql_operator_managed_resources` | Gauge | Number of managed resources by type and namespace |

### Business Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `mssql_database_ready` | Gauge | Whether a database CR is Ready (1/0) |
| `mssql_login_ready` | Gauge | Whether a login CR is Ready (1/0) |
| `mssql_server_connected` | Gauge | Whether a SQLServer can be reached (1/0) |
| `mssql_backup_last_success_timestamp` | Gauge | Unix timestamp of last successful scheduled backup |
| `mssql_scheduled_backup_total` | Gauge | Total backups from a scheduled backup by result |
| `mssql_ag_replica_synchronized` | Gauge | Whether an AG replica is synchronized (1/0) |

### Enabling ServiceMonitor

```yaml
# values.yaml
metrics:
  enabled: true
  serviceMonitor:
    enabled: true
    interval: "30s"
    labels:
      release: prometheus  # match your Prometheus selector
```

## Alerts (PrometheusRule)

Enable pre-configured alerts:

```yaml
metrics:
  prometheusRule:
    enabled: true
    labels:
      release: prometheus
```

### Built-in Alerts

| Alert | Severity | Description |
|-------|----------|-------------|
| `MSSQLDatabaseNotReady` | warning | Database CR not ready for >5 min |
| `MSSQLBackupOverdue` | critical | No successful backup for >25 hours |
| `MSSQLBackupFailing` | warning | >2 backup failures in 24 hours |
| `MSSQLAGReplicaNotSynchronized` | warning | AG replica out of sync for >10 min |
| `MSSQLServerDisconnected` | critical | Cannot connect to SQL Server for >3 min |
| `MSSQLOperatorReconcileErrors` | warning | Sustained reconciliation errors |

### Custom Alerts

Add custom rules via `metrics.prometheusRule.additionalRules`:

```yaml
metrics:
  prometheusRule:
    enabled: true
    additionalRules:
      - alert: MSSQLTooManyDatabases
        expr: count(mssql_database_ready) > 100
        for: 0m
        labels:
          severity: info
        annotations:
          summary: "More than 100 managed databases"
```

## Events

The operator emits Kubernetes events on CRs for all significant actions:

```bash
kubectl describe database myapp-db
# Events:
#   Type    Reason              Message
#   ----    ------              -------
#   Normal  DatabaseCreated     Database myapp created
#   Normal  RecoveryModelUpdated Database myapp recovery model set to Full
```

Key event types: `DatabaseCreated`, `DatabaseDropped`, `LoginCreated`, `BackupCompleted`, `BackupFailed`, `ReconciliationFailed`, `ConnectionFailed`.

## Health Probes

- Liveness: `GET :8081/healthz`
- Readiness: `GET :8081/readyz`

## Structured Logging

Logs are structured JSON via `zap`. Set log level:

```yaml
# In deployment args
- --zap-log-level=debug  # debug, info (default), error
```
