package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsRegistered(t *testing.T) {
	// Verify all metrics can be described (i.e., they are well-formed)
	ch := make(chan *prometheus.Desc, 10)
	go func() {
		ReconcileTotal.Describe(ch)
		ReconcileErrors.Describe(ch)
		ReconcileDuration.Describe(ch)
		ManagedResources.Describe(ch)
		close(ch)
	}()

	count := 0
	for range ch {
		count++
	}
	if count != 4 {
		t.Errorf("expected 4 metric descriptors, got %d", count)
	}
}

func TestReconcileTotal_Increment(t *testing.T) {
	ReconcileTotal.WithLabelValues("Database", "success").Inc()

	// Just verify it doesn't panic — we can't easily read values from prometheus counters
	// without a full registry collect cycle
}

func TestReconcileErrors_Increment(t *testing.T) {
	ReconcileErrors.WithLabelValues("Login", "ConnectionFailed").Inc()
}

func TestReconcileDuration_Observe(t *testing.T) {
	ReconcileDuration.WithLabelValues("DatabaseUser").Observe(0.5)
}

func TestManagedResources_Set(t *testing.T) {
	ManagedResources.WithLabelValues("Database", "default").Set(5)
}
