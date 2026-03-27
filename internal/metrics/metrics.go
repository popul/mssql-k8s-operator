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
)

func init() {
	metrics.Registry.MustRegister(
		ReconcileTotal,
		ReconcileErrors,
		ReconcileDuration,
		ManagedResources,
	)
}
