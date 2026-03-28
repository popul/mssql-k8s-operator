# How to configure Always On Availability Groups

## Prerequisites

- SQL Server 2019+ Enterprise Edition (or Developer Edition for testing)
- HADR enabled on each SQL Server instance (`ALTER SERVER CONFIGURATION SET HADR ON`)
- Each SQL Server instance must be reachable from the others on port 5022 (mirroring endpoint)
- Databases to include in the AG must already exist on the primary and use the full recovery model

## Create an Availability Group

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: AvailabilityGroup
metadata:
  name: myapp-ag
spec:
  agName: myag
  replicas:
    - serverName: sql-0
      endpointURL: "TCP://sql-0.sql-headless.mssql.svc:5022"
      availabilityMode: SynchronousCommit
      failoverMode: Automatic
      seedingMode: Automatic
      secondaryRole: No
      server:
        host: sql-0.sql-headless.mssql.svc
        credentialsSecret:
          name: sa-credentials
    - serverName: sql-1
      endpointURL: "TCP://sql-1.sql-headless.mssql.svc:5022"
      availabilityMode: SynchronousCommit
      failoverMode: Automatic
      seedingMode: Automatic
      secondaryRole: AllowReadIntentOnly
      server:
        host: sql-1.sql-headless.mssql.svc
        credentialsSecret:
          name: sa-credentials
  databases:
    - name: mydb
  listener:
    name: ag-listener
    port: 1433
  automatedBackupPreference: Secondary
  dbFailover: true
```

## What the operator does

When you create this CR, the operator:

1. **Connects to the primary** (first replica in the list)
2. **Creates a HADR endpoint** on port 5022 (if not present)
3. **Creates the Availability Group** via `CREATE AVAILABILITY GROUP`
4. **Connects to each secondary** and:
   - Creates a HADR endpoint on that instance
   - Joins the AG (`ALTER AVAILABILITY GROUP ... JOIN`)
   - Grants `CREATE ANY DATABASE` for automatic seeding
5. **Adds databases** to the AG
6. **Creates the listener** (if specified)
7. **Continuously monitors** AG health and updates status

## Check AG status

```bash
# Quick overview
kubectl get msag

# Detailed status with replica and database states
kubectl describe msag myapp-ag
```

The status shows:
- **primaryReplica**: which instance is currently primary
- **replicas[].role**: PRIMARY or SECONDARY
- **replicas[].synchronizationState**: SYNCHRONIZED, SYNCHRONIZING, or NOT_SYNCHRONIZING
- **replicas[].connected**: whether the replica is connected
- **databases[].joined**: whether the database has joined the AG

## Add a read-only secondary (async, DR)

Add a third replica with asynchronous commit for disaster recovery:

```yaml
spec:
  replicas:
    # ... existing replicas ...
    - serverName: sql-2
      endpointURL: "TCP://sql-2.sql-dr.svc:5022"
      availabilityMode: AsynchronousCommit
      failoverMode: Manual
      seedingMode: Automatic
      secondaryRole: AllowReadIntentOnly
      server:
        host: sql-2.sql-dr.svc
        credentialsSecret:
          name: sa-credentials-dr
```

Note: `Automatic` failover requires `SynchronousCommit`. Async replicas must use `Manual` failover.

## Add a database to an existing AG

Simply add it to the `databases` list in the spec:

```yaml
spec:
  databases:
    - name: mydb
    - name: mydb2   # new database
```

The operator will add it on the next reconciliation.

## Secondary role options

| Value | Description |
|---|---|
| `No` | No connections allowed on the secondary |
| `AllowReadIntentOnly` | Only read-intent connections (for read scale-out) |
| `AllowAllConnections` | All connections allowed (read-only) |

## Seeding modes

| Mode | Description |
|---|---|
| `Automatic` | SQL Server copies data to secondaries automatically. Simplest option. |
| `Manual` | You must backup/restore the database on secondaries before joining. Required for very large databases. |

## Deletion

Deleting the `AvailabilityGroup` CR drops the AG on SQL Server (`DROP AVAILABILITY GROUP`). The databases themselves are **not** deleted — they remain as standalone databases on each instance.

## Limitations

- The operator does not deploy SQL Server instances (use a StatefulSet or the Microsoft operator)
- Manual failover (`ALTER AVAILABILITY GROUP ... FAILOVER`) is not managed by the operator
- Listener IP addresses are static — on Kubernetes, prefer using a Service instead
