# Helm Values Reference

All values for the `mssql-operator` Helm chart.

## Operator

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `replicaCount` | int | `1` | Number of operator replicas. Use 2+ for HA. |
| `leaderElection.enabled` | bool | `true` | Enable leader election. Required when `replicaCount > 1`. |

## Image

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `image.repository` | string | `ghcr.io/popul/mssql-k8s-operator` | Container image repository |
| `image.tag` | string | Chart `appVersion` | Image tag. Empty defaults to Chart appVersion. |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `imagePullSecrets` | list | `[]` | Image pull secrets |

## Naming

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `nameOverride` | string | `""` | Override the chart name |
| `fullnameOverride` | string | `""` | Override the full release name |

## Service Account

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `serviceAccount.create` | bool | `true` | Create a ServiceAccount |
| `serviceAccount.name` | string | `""` | Name override. Empty uses the release name. |
| `serviceAccount.annotations` | map | `{}` | Annotations on the ServiceAccount |

## Resources

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `resources.requests.cpu` | string | `10m` | CPU request |
| `resources.requests.memory` | string | `64Mi` | Memory request |
| `resources.limits.cpu` | string | `500m` | CPU limit |
| `resources.limits.memory` | string | `128Mi` | Memory limit |

## Scheduling

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `nodeSelector` | map | `{}` | Node selector |
| `tolerations` | list | `[]` | Tolerations |
| `affinity` | map | `{}` | Affinity rules |

## Pod Security

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `podAnnotations` | map | `{}` | Annotations on the operator pod |
| `podSecurityContext.runAsNonRoot` | bool | `true` | Require non-root |
| `podSecurityContext.seccompProfile.type` | string | `RuntimeDefault` | Seccomp profile |
| `securityContext.allowPrivilegeEscalation` | bool | `false` | Disallow privilege escalation |
| `securityContext.readOnlyRootFilesystem` | bool | `true` | Read-only root filesystem |
| `securityContext.capabilities.drop` | list | `["ALL"]` | Dropped capabilities |

## Metrics

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `metrics.enabled` | bool | `true` | Expose Prometheus metrics |
| `metrics.port` | int | `8080` | Metrics endpoint port |
| `metrics.serviceMonitor.enabled` | bool | `false` | Create a Prometheus ServiceMonitor |
| `metrics.serviceMonitor.interval` | string | `"30s"` | Scrape interval |
| `metrics.serviceMonitor.labels` | map | `{}` | Additional labels on the ServiceMonitor |

## Health

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `health.port` | int | `8081` | Health/readiness probe port |

## Network Policy

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `networkPolicy.enabled` | bool | `false` | Create a NetworkPolicy for the operator |
| `networkPolicy.apiServerCIDR` | string | `""` | CIDR for Kubernetes API server egress |
| `networkPolicy.sqlServerCIDR` | string | `""` | CIDR for SQL Server egress |
