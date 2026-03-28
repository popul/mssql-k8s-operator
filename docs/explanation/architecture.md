# Architecture

This document explains how the MSSQL Kubernetes Operator works internally, the design decisions behind it, and the trade-offs involved.

## What is a Kubernetes operator?

A Kubernetes operator extends the API with Custom Resource Definitions (CRDs) and runs controllers that continuously reconcile the declared state with the actual state of an external system. In our case, the external system is Microsoft SQL Server.

The operator does not deploy or manage the SQL Server instance itself. It manages **objects inside SQL Server** (databases, logins, users, schemas, permissions) by connecting via TDS (the SQL Server wire protocol) and executing T-SQL statements.

## Reconciliation loop

Each controller follows the same pattern:

```
1. Fetch    — Get the CR from the Kubernetes API. If deleted, return.
2. Finalize — Add the finalizer if absent. If DeletionTimestamp is set, run cleanup.
3. Observe  — Query SQL Server for the current state.
4. Compare  — Diff the desired state (spec) against the observed state.
5. Act      — Execute DDL/DCL to converge (CREATE, ALTER, GRANT, etc.).
6. Report   — Update the status conditions and ObservedGeneration.
```

This is **level-triggered**, not edge-triggered. The controller does not react to individual events. It compares the full desired state to the full observed state on every reconciliation. This makes the system self-healing: if someone manually changes something on SQL Server, the next reconciliation detects the drift and corrects it.

## Why level-triggered?

Edge-triggered controllers react to events: "the user changed field X, so apply change X." This seems efficient but is fragile. If an event is missed, or if the controller restarts mid-operation, the system can drift permanently.

Level-triggered controllers ask: "what is the desired state, what is the current state, and what do I need to do to make them match?" This is inherently idempotent and resilient to crashes, restarts, and missed events.

The cost is that each reconciliation must query SQL Server for the current state. This is acceptable because SQL Server metadata queries (`sys.databases`, `sys.server_principals`, etc.) are fast.

## Finalizers

Kubernetes garbage-collects objects immediately unless a finalizer is present. The operator adds the finalizer `mssql.popul.io/finalizer` on first reconciliation.

When a CR is deleted, Kubernetes sets `DeletionTimestamp` but does not remove the object. The controller sees this, runs cleanup (e.g., `DROP DATABASE`), removes the finalizer, and Kubernetes completes the deletion.

This ensures cleanup happens even if the user runs `kubectl delete` -- the SQL Server objects are not orphaned.

### Deletion safety

- `deletionPolicy: Retain` (default) skips the SQL Server cleanup entirely. The finalizer is removed and the CR disappears, but the database/login/schema continues to exist on SQL Server.
- `deletionPolicy: Delete` runs the SQL Server cleanup before removing the finalizer.
- If cleanup fails due to a transient error (connection lost), the controller logs the error and removes the finalizer anyway. This prevents CRs from being stuck in `Terminating` forever.

The behavior for structural blockers varies by controller:

| Controller | Blocker | Behavior |
|------------|---------|----------|
| Login | Login has dependent database users | Keeps finalizer, requeues until users are deleted |
| DatabaseUser | User owns objects in the database | Keeps finalizer, requeues until ownership is transferred |
| Schema | Schema contains objects | Keeps finalizer, requeues until objects are moved/dropped |
| Database | None | Always removes finalizer (logs error if DROP fails) |
| Permission | None | Always removes finalizer (logs error if REVOKE fails) |

Database and Permission controllers always remove the finalizer on deletion because there is no user-actionable blocker — a failed DROP or REVOKE is typically a transient issue that should not leave the CR stuck indefinitely.

## Secret watches

The operator watches Kubernetes Secrets in addition to its own CRDs. When a Secret changes, the operator lists all CRs in the same namespace that reference that Secret and re-reconciles them.

This is how password rotation works for logins: update the Secret, and the operator detects the change and calls `ALTER LOGIN ... WITH PASSWORD`.

A subtlety: the `GenerationChangedPredicate` is applied only to CRD watches (via `builder.WithPredicates`), not globally. This ensures Secret changes are not filtered out.

## Error handling strategy

The operator distinguishes two types of errors:

**Transient errors** (connection lost, timeout, deadlock): the controller returns the error to controller-runtime, which requeues with exponential backoff. No status condition is set because the error may resolve on its own.

**Permanent errors** (invalid configuration, missing Secret, immutable field changed): the controller sets `Ready=False` with a specific `Reason` and does **not** return an error. This prevents infinite retry loops on errors that cannot resolve without user intervention.

## SQL injection prevention

The operator builds DDL statements dynamically (e.g., `CREATE DATABASE [name]`). SQL Server does not support parameterized identifiers in DDL.

To prevent SQL injection:

- All identifiers (database names, login names, schema names, role names) are escaped using `QuoteName()`, which wraps the name in `[brackets]` and escapes embedded `]` characters. This is equivalent to T-SQL's `QUOTENAME()`.
- String values (passwords) are escaped using `QuoteString()`, which doubles single quotes and wraps in `N'...'`.
- Permission keywords (SELECT, INSERT, etc.) are validated against a whitelist (`IsValidPermission()`). Invalid keywords are rejected before any SQL is executed.
- Permission targets (`SCHEMA::app`) are parsed and each component is quoted separately via `QuotePermissionTarget()`.

## Why one condition type?

The operator uses a single `Ready` condition instead of multiple conditions (e.g., `Connected`, `Provisioned`, `RolesConfigured`).

The rationale: multiple conditions create ambiguity for consumers. If `Connected=True` but `Provisioned=False`, is the resource ready? With a single `Ready` condition, the answer is always clear. The `Reason` field (e.g., `ConnectionFailed`, `SecretNotFound`, `SchemaNotEmpty`) provides the detail needed for debugging.

This follows the convention used by many Kubernetes operators and aligns with the Kubernetes API conventions recommendation that conditions should represent the resource's readiness from the user's perspective.

## Requeue strategy

| Situation | Behavior |
|-----------|----------|
| Reconciliation succeeded | `RequeueAfter: 30s` (with ±20% jitter) |
| Transient error | Return error, controller-runtime applies exponential backoff |
| Permanent error | Set condition, return `Result{}` (no requeue, no error) |
| Deletion blocked | `RequeueAfter: 30s` (with jitter) |

The 30-second periodic requeue serves two purposes:
1. Detect drift between the declared state and SQL Server (e.g., someone manually dropped a database)
2. Retry blocked deletions (e.g., a user still owns objects)

The ±20% jitter prevents thundering herd when many CRs are reconciled simultaneously.

## Why not use owner references between CRDs?

A `DatabaseUser` references a `Login`, but we do not set an owner reference from the user to the login. This is intentional.

Owner references would cause the `DatabaseUser` to be garbage-collected when the `Login` is deleted. But the SQL Server user might still exist and need explicit cleanup. Instead, the login controller checks for dependent users before deletion and blocks if any exist.

This gives the user explicit control over the deletion order and avoids surprise cascading deletes.
