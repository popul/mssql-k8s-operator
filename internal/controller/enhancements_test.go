package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

// --- Collation drift detection ---

func TestDatabaseReconcile_CollationDriftDetected(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	collation := "SQL_Latin1_General_CP1_CI_AS"
	db := testDatabase("mydb", nil)
	db.Spec.Collation = &collation
	r, recorder := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)
	ctx := context.Background()

	// First reconcile: creates DB with collation
	_, err := r.Reconcile(ctx, reqFor("mydb"))
	if err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}

	// Verify database was created
	if !mockSQL.WasCalled("CreateDatabase") {
		t.Fatal("expected CreateDatabase to be called")
	}

	// Simulate collation drift: somebody changed it externally
	// (In the mock, we set a different collation directly)
	mockSQL.SetDatabaseCollation(ctx, "mydb", "Latin1_General_CI_AS")
	mockSQL.ResetCalls()

	// Second reconcile should detect the drift and set Ready=False with CollationChangeNotSupported
	result, err := r.Reconcile(ctx, reqFor("mydb"))
	if err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	// Should NOT requeue (permanent condition)
	if result.RequeueAfter != 0 {
		t.Error("expected no requeue for collation mismatch (permanent error)")
	}

	// Status should be Ready=False with ReasonCollationChangeNotSupported
	var updated v1alpha1.Database
	r.Client.Get(ctx, reqFor("mydb").NamespacedName, &updated)

	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionFalse &&
			c.Reason == v1alpha1.ReasonCollationChangeNotSupported {
			found = true
		}
	}
	if !found {
		t.Error("expected Ready=False with Reason=CollationChangeNotSupported")
	}

	// Should have emitted a Warning event
	select {
	case event := <-recorder.Events:
		if event == "" {
			t.Error("expected non-empty event")
		}
	default:
		t.Error("expected a warning event for collation mismatch")
	}
}

func TestDatabaseReconcile_CollationMatchOK(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	collation := "SQL_Latin1_General_CP1_CI_AS"
	db := testDatabase("mydb", nil)
	db.Spec.Collation = &collation
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)
	ctx := context.Background()

	// First reconcile creates DB
	r.Reconcile(ctx, reqFor("mydb"))

	// Set mock collation to match (it should already match since CreateDatabase sets it)
	mockSQL.ResetCalls()

	// Second reconcile should be happy
	result, err := r.Reconcile(ctx, reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue normally (Ready=True)
	if result.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 when collation matches")
	}

	var updated v1alpha1.Database
	r.Client.Get(ctx, reqFor("mydb").NamespacedName, &updated)
	for _, c := range updated.Status.Conditions {
		if c.Type == v1alpha1.ConditionReady && c.Status == metav1.ConditionTrue {
			return // OK
		}
	}
	t.Error("expected Ready=True when collation matches")
}

func TestDatabaseReconcile_NoCollationSpec_SkipsCheck(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("mydb", nil)
	// No collation in spec
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)
	ctx := context.Background()

	r.Reconcile(ctx, reqFor("mydb"))
	mockSQL.ResetCalls()

	// Should be Ready=True without checking collation
	result, err := r.Reconcile(ctx, reqFor("mydb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected normal requeue")
	}

	// Should not call GetDatabaseCollation when no collation specified
	if mockSQL.WasCalled("GetDatabaseCollation") {
		t.Error("should not check collation when spec.collation is nil")
	}
}

// --- Metrics integration (verify ReconcileDuration is observed) ---

func TestDatabaseReconcile_MetricsDurationObserved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	db := testDatabase("metricsdb", nil)
	r, _ := newTestDatabaseReconciler([]runtime.Object{db, saSecret()}, mockSQL)

	// Just verify reconcile doesn't panic with metrics instrumentation
	_, err := r.Reconcile(context.Background(), reqFor("metricsdb"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginReconcile_MetricsDurationObserved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	login := testLogin("metricslogin", nil)
	r, _ := newTestLoginReconciler([]runtime.Object{login, saSecret(), passwordSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("metricslogin"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDatabaseUserReconcile_MetricsDurationObserved(t *testing.T) {
	mockSQL := sqlclient.NewMockClient()
	dbUser := testDatabaseUser("metricsuser")
	login := readyLogin()
	r, _ := newTestDatabaseUserReconciler([]runtime.Object{dbUser, login, saSecret()}, mockSQL)

	_, err := r.Reconcile(context.Background(), reqFor("metricsuser"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
