package main

import (
	"flag"
	"os"

	bootstrap "github.com/ntlaletsi70/blanketops-environments-controller/internal/bootstrap"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {

	bootstrap.RegisterSchemes(scheme)
}

func main() {
	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Probe bind address")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "ed9f0ef9.blanketops.dev",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := bootstrap.RegisterControllers(mgr); err != nil {
		setupLog.Error(err, "failed to register controllers")
		os.Exit(1)
	}

	if err := bootstrap.RegisterObservers(mgr); err != nil {
		setupLog.Error(err, "failed to register observers")
		os.Exit(1)
	}

	if err := bootstrap.RegisterBuild(mgr, setupLog, mgr.GetEventRecorderFor("blanketops")); err != nil {
		setupLog.Error(err, "failed to register build subsystem")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add health check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
