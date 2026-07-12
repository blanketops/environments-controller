/*
Copyright 2026 The BlanketOps Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// main.go is the controller's process entry point.
//
// It builds the runtime scheme (bootstrap.RegisterSchemes, run from
// init), constructs the controller-runtime manager, builds this
// controller's Runtime (internal/runtime), and registers the CQRS
// controllers, observers, and build subsystem through internal/bootstrap
// before starting the manager.
package main

import (
	"flag"
	"os"

	"github.com/BlanketOps/blanketops-environments/core"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	runtimeinfra "github.com/BlanketOps/blanketops-environments-controller/internal/runtime"
	bootstrap "github.com/BlanketOps/environments-controller/internal/bootstrap"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// init registers every API type with the package-level scheme before
// main constructs the manager.
func init() {
	bootstrap.RegisterSchemes(scheme)
}

// Runtime describes read access to the core engine's Engine, Cache,
// Events, and Registry subsystems.
type Runtime interface {
	Engine() *core.Engine
	Cache() *core.Cache
	Events() *core.EventRecorder
	Registry() *core.Registry
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

	// ---------------------------------------------------
	// Create Runtime
	// ---------------------------------------------------

	rt := runtimeinfra.New(mgr)

	// ---------------------------------------------------
	// Controllers
	// ---------------------------------------------------

	if err := bootstrap.RegisterControllers(mgr, rt); err != nil {
		setupLog.Error(err, "failed to register controllers")
		os.Exit(1)
	}

	if err := bootstrap.RegisterObservers(mgr); err != nil {
		setupLog.Error(err, "failed to register observers")
		os.Exit(1)
	}

	if err := bootstrap.RegisterBuild(mgr, rt, setupLog, mgr.GetEventRecorder("blanketops-environments")); err != nil {
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
