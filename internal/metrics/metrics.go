package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// ReconcileTotal counts total reconciliation attempts per controller and result.
	ReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mssql_operator",
			Name:      "reconcile_total",
			Help:      "Total number of reconciliation attempts",
		},
		[]string{"controller", "result"},
	)

	// ReconcileErrors counts reconciliation errors per controller and reason.
	ReconcileErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mssql_operator",
			Name:      "reconcile_errors_total",
			Help:      "Total number of reconciliation errors",
		},
		[]string{"controller", "reason"},
	)

	// ReconcileDuration tracks reconciliation duration per controller.
	ReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mssql_operator",
			Name:      "reconcile_duration_seconds",
			Help:      "Duration of reconciliation in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"controller"},
	)

	// ManagedResources tracks the number of managed resources per type and namespace.
	ManagedResources = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mssql_operator",
			Name:      "managed_resources",
			Help:      "Number of managed SQL Server resources",
		},
		[]string{"type", "namespace"},
	)

	// --- Business metrics ---

	// DatabaseState tracks the state of each managed database.
	DatabaseState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mssql",
			Name:      "database_ready",
			Help:      "Whether the database CR is in Ready state (1=ready, 0=not ready)",
		},
		[]string{"name", "namespace", "database", "server"},
	)

	// BackupLastSuccess records the timestamp of the last successful backup.
	BackupLastSuccess = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mssql",
			Name:      "backup_last_success_timestamp",
			Help:      "Unix timestamp of the last successful backup for a scheduled backup",
		},
		[]string{"name", "namespace", "database"},
	)

	// BackupTotal counts total backups per scheduled backup and result.
	BackupTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mssql",
			Name:      "scheduled_backup_total",
			Help:      "Total number of backups from a scheduled backup",
		},
		[]string{"name", "namespace", "result"},
	)

	// AGReplicaLag tracks the synchronization state of AG replicas.
	AGReplicaLag = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mssql",
			Name:      "ag_replica_synchronized",
			Help:      "Whether an AG replica is synchronized (1=synchronized, 0=not)",
		},
		[]string{"ag_name", "namespace", "replica", "role"},
	)

	// SQLServerConnected tracks whether a SQLServer CR can connect.
	SQLServerConnected = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mssql",
			Name:      "server_connected",
			Help:      "Whether the operator can connect to the SQL Server (1=connected, 0=not)",
		},
		[]string{"name", "namespace", "host"},
	)

	// FencingTotal counts fencing actions to resolve split-brain.
	FencingTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mssql_operator",
			Name:      "fencing_total",
			Help:      "Total fencing actions to resolve split-brain",
		},
		[]string{"ag_name", "namespace", "fenced_replica", "type"},
	)

	// LoginCount tracks the number of managed logins.
	LoginCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mssql",
			Name:      "login_ready",
			Help:      "Whether the login CR is in Ready state (1=ready, 0=not ready)",
		},
		[]string{"name", "namespace", "login"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		ReconcileTotal,
		ReconcileErrors,
		ReconcileDuration,
		ManagedResources,
		DatabaseState,
		BackupLastSuccess,
		BackupTotal,
		AGReplicaLag,
		FencingTotal,
		SQLServerConnected,
		LoginCount,
	)
}
