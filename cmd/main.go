package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	"github.com/popul/mssql-k8s-operator/internal/controller"
	_ "github.com/popul/mssql-k8s-operator/internal/metrics" // register Prometheus metrics
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var enableWebhooks bool
	var leaseDuration time.Duration
	var renewDeadline time.Duration
	var retryPeriod time.Duration

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager, ensuring only one active controller manager.")
	flag.BoolVar(&enableWebhooks, "enable-webhooks", false,
		"Enable admission webhooks (requires cert-manager).")
	flag.DurationVar(&leaseDuration, "leader-elect-lease-duration", 15*time.Second,
		"Duration that non-leader candidates will wait to force acquire leadership.")
	flag.DurationVar(&renewDeadline, "leader-elect-renew-deadline", 10*time.Second,
		"Duration that the acting leader will retry refreshing leadership before giving up.")
	flag.DurationVar(&retryPeriod, "leader-elect-retry-period", 2*time.Second,
		"Duration between leader election retries.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "mssql-operator.popul.io",
		LeaseDuration:                 &leaseDuration,
		RenewDeadline:                 &renewDeadline,
		RetryPeriod:                   &retryPeriod,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	sqlFactory := sqlclient.NewClientFactory()

	if err := (&controller.DatabaseReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("database-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Database")
		os.Exit(1)
	}

	if err := (&controller.LoginReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("login-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Login")
		os.Exit(1)
	}

	if err := (&controller.DatabaseUserReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("databaseuser-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DatabaseUser")
		os.Exit(1)
	}

	if err := (&controller.SchemaReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("schema-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Schema")
		os.Exit(1)
	}

	if err := (&controller.PermissionReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("permission-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Permission")
		os.Exit(1)
	}

	if err := (&controller.AvailabilityGroupReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("availabilitygroup-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AvailabilityGroup")
		os.Exit(1)
	}

	if err := (&controller.AGFailoverReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("agfailover-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AGFailover")
		os.Exit(1)
	}

	if err := (&controller.BackupReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("backup-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Backup")
		os.Exit(1)
	}

	if err := (&controller.RestoreReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("restore-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Restore")
		os.Exit(1)
	}

	if err := (&controller.SQLServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("sqlserver-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SQLServer")
		os.Exit(1)
	}

	if err := (&controller.ScheduledBackupReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("scheduledbackup-controller"),
		SQLClientFactory: sqlFactory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ScheduledBackup")
		os.Exit(1)
	}

	if enableWebhooks {
		setupLog.Info("registering webhooks")
		// Webhooks are registered via the types' Default/Validate methods.
		// Requires cert-manager for TLS certificate provisioning.
		// TODO: Register webhooks with mgr.GetWebhookServer() when
		// webhook types implement admission.Handler interface.
		setupLog.Info("webhooks enabled — ensure cert-manager is installed")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
