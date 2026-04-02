# How to install the MSSQL Operator

## Install with Helm

```bash
helm install mssql-operator oci://ghcr.io/popul/mssql-k8s-operator/charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace
```

> The operator image (`ghcr.io/popul/mssql-k8s-operator`) is pulled automatically by the chart.

## Install with custom values

```bash
helm install mssql-operator oci://ghcr.io/popul/mssql-k8s-operator/charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace \
  --set replicaCount=2 \
  --set leaderElection.enabled=true \
  --set metrics.serviceMonitor.enabled=true
```

Or use a values file:

```bash
helm install mssql-operator oci://ghcr.io/popul/mssql-k8s-operator/charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace \
  -f my-values.yaml
```

See [Helm values reference](../reference/helm-values.md) for all options.

## Install from source (development)

```bash
helm install mssql-operator ./charts/mssql-operator \
  --namespace mssql-operator-system \
  --create-namespace
```

## Upgrade

```bash
helm upgrade mssql-operator oci://ghcr.io/popul/mssql-k8s-operator/charts/mssql-operator \
  --namespace mssql-operator-system
```

Helm does not update CRDs on upgrade. To update CRDs manually:

```bash
helm pull oci://ghcr.io/popul/mssql-k8s-operator/charts/mssql-operator \
  --untar
kubectl apply -f mssql-operator/crds/
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
  enabled: true        # default
  leaseDuration: 15s   # time before standby forces leadership acquisition
  renewDeadline: 10s   # time leader has to renew before giving up
  retryPeriod: 2s      # interval between election retries
```

Only one replica actively reconciles (the **leader**). The others wait as **standby**. If the leader pod dies, a standby acquires the lease and takes over within ~15 seconds.

When `replicaCount > 1`, the Helm chart automatically configures:

- **Pod anti-affinity** (preferred): spreads replicas across different nodes
- **Topology spread constraints**: distributes pods across availability zones (`ScheduleAnyway`)
- **PodDisruptionBudget**: `minAvailable: 1` to prevent voluntary disruptions from taking all replicas down

You can override any of these with custom `affinity`, `topologySpreadConstraints`, or by editing the PDB template.

### Tuning the lease

| Parameter | Default | Effect |
|---|---|---|
| `leaseDuration` | `15s` | Max time before a standby can try to become leader |
| `renewDeadline` | `10s` | Max time the leader retries renewing before stepping down |
| `retryPeriod` | `2s` | How often candidates check the lease |

Lower values = faster failover but more API server load. The defaults (15s/10s/2s) are a good production baseline.

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
