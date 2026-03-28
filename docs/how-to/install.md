# How to install the MSSQL Operator

## Install with Helm

```bash
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace
```

## Install with custom values

```bash
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace \
  --set replicaCount=2 \
  --set leaderElection.enabled=true \
  --set metrics.serviceMonitor.enabled=true
```

Or use a values file:

```bash
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace \
  -f my-values.yaml
```

See [Helm values reference](../reference/helm-values.md) for all options.

## Upgrade

```bash
helm upgrade mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system
```

Helm does not update CRDs on upgrade. To update CRDs manually:

```bash
kubectl apply -f charts/mssql-operator/crds/
```

## Uninstall

```bash
helm uninstall mssql-operator --namespace mssql-operator-system
```

CRDs and their instances are not deleted by Helm uninstall. Remove them manually if needed:

```bash
kubectl delete crd databases.mssql.popul.io logins.mssql.popul.io \
  databaseusers.mssql.popul.io schemas.mssql.popul.io permissions.mssql.popul.io
```

## Enable high availability

Run 2+ replicas with leader election:

```yaml
replicaCount: 2
leaderElection:
  enabled: true
```

Only one replica actively reconciles. The others wait as standby. Failover is automatic.

## Enable Prometheus monitoring

```yaml
metrics:
  enabled: true
  serviceMonitor:
    enabled: true
    labels:
      release: prometheus   # match your Prometheus operator selector
```

## Restrict network access

```yaml
networkPolicy:
  enabled: true
  apiServerCIDR: "10.96.0.0/12"
  sqlServerCIDR: "10.0.0.0/8"
```

This creates a `NetworkPolicy` allowing only egress to the Kubernetes API server and your SQL Server instances.
