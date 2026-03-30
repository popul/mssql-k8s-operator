# How to troubleshoot

## CR stuck in Ready=False

Check the status conditions:

```bash
kubectl describe msdb myapp-db
```

Look at the `Conditions` section and `Events`. The `Reason` field tells you what went wrong:

| Reason | What to do |
|--------|-----------|
| `SecretNotFound` | Create the Secret referenced in `credentialsSecret` |
| `InvalidCredentialsSecret` | Ensure the Secret has `username` and `password` keys |
| `ConnectionFailed` | Verify SQL Server is reachable (see below) |
| `LoginRefNotFound` | Create the Login CR referenced in `loginRef` |
| `LoginNotReady` | Wait for the Login CR to become Ready |
| `DeploymentProvisioning` | Managed SQLServer: StatefulSet pods are not yet ready |
| `CertificatesProvisioning` | Managed SQLServer: HADR certificates are being provisioned |
| `AGProvisioning` | Managed SQLServer: Availability Group is being created |
| `EULANotAccepted` | Managed SQLServer: set `instance.acceptEULA: true` |

## Managed SQLServer not becoming Ready

If your managed `SQLServer` CR is stuck:

1. Check the StatefulSet status:

```bash
kubectl get sts mssql -n mssql
kubectl describe sts mssql -n mssql
```

2. Check pod logs:

```bash
kubectl logs mssql-0 -n mssql
```

3. Common causes:
   - **Insufficient memory**: SQL Server requires at least 2Gi
   - **Storage provisioning failed**: check PVC status with `kubectl get pvc -n mssql`
   - **SA password too weak**: must meet SQL Server complexity requirements
   - **EULA not accepted**: set `instance.acceptEULA: true`

4. For cluster mode (replicas > 1), check certificates:

```bash
kubectl get secrets -n mssql | grep cert
```

## CR stuck in Terminating

A CR can be stuck in `Terminating` if its finalizer cannot complete. Common causes:

**Login has dependent users:**

```bash
# Check which DatabaseUsers reference this login
kubectl get msuser -o jsonpath='{range .items[?(@.spec.loginRef.name=="myapp-login")]}{.metadata.name}{"\n"}{end}'
```

Delete the dependent `DatabaseUser` CRs first.

**User owns objects in the database:**

Connect to SQL Server and transfer ownership:

```sql
ALTER AUTHORIZATION ON SCHEMA::[myschema] TO [dbo];
```

The operator requeues periodically and will complete deletion once the blocker is resolved.

**Schema contains objects:**

Move or drop the objects in the schema, then the Schema CR deletion will proceed.

## SQL Server connection errors

1. Check the SQLServer CR status:

```bash
kubectl get sqlsrv -n mssql
kubectl describe sqlsrv mssql -n mssql
```

2. For managed instances, verify the pod is running:

```bash
kubectl get pods -n mssql -l app.kubernetes.io/instance=mssql
```

3. For external instances, verify connectivity from inside the cluster:

```bash
kubectl run test-sql --rm -it --image=mcr.microsoft.com/mssql-tools -- \
  /opt/mssql-tools/bin/sqlcmd -S mssql.mssql.svc.cluster.local -U sa -P 'password'
```

4. Check the credentials Secret exists and has the correct keys:

```bash
kubectl get secret sa-credentials -o jsonpath='{.data}' | jq
```

5. If `NetworkPolicy` is enabled, ensure egress to SQL Server port 1433 is allowed.

6. If TLS is enabled (`tls: true`), ensure the SQL Server certificate is trusted.

## Operator not reconciling

1. Check operator pod logs:

```bash
kubectl logs -n mssql-operator-system deploy/mssql-operator
```

2. Verify CRDs are installed:

```bash
kubectl get crd databases.mssql.popul.io sqlservers.mssql.popul.io
```

3. Check RBAC:

```bash
kubectl auth can-i get secrets \
  --as=system:serviceaccount:mssql-operator-system:mssql-operator
```

4. Check leader election (if running multiple replicas):

```bash
kubectl get lease -n mssql-operator-system
```

## Reconciliation is slow

The operator reconciles every ~30 seconds (with jitter). If changes seem slow:

- Check the operator logs for errors causing requeue backoff
- Ensure SQL Server is responding within 30 seconds (the SQL operation timeout)
- Check the `mssql_operator_reconcile_duration_seconds` metric for latency

## Password rotation not detected

The operator watches Secrets and re-reconciles when they change. If password rotation seems stuck:

1. Verify the Secret was actually updated:

```bash
kubectl get secret myapp-login-password -o jsonpath='{.metadata.resourceVersion}'
```

2. Check the Login status for `passwordSecretResourceVersion` -- it should match the Secret's `resourceVersion`.

3. Check operator logs for `LOGIN_PASSWORD_ROTATED` or connection errors.
