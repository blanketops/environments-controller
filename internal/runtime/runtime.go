package runtime

import (
	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/core"
	ctrl "sigs.k8s.io/controller-runtime"
)

type Runtime struct {
	Cache    *core.Cache
	Events   *core.EventRecorder
	Registry *core.Registry
	Engine   *core.Engine
	Log      logr.Logger
}

func New(mgr ctrl.Manager) *Runtime {

	log := ctrl.Log.WithName("runtime")

	cache := core.NewCache(mgr, nil)
	registry := core.NewRegistry()

	engine := core.NewEngine(
		registry,
		ctrl.Log.WithName("engine"),
	)

	events := core.NewEventRecorder(
		mgr.GetEventRecorder("blanketops-runtime"),
	)

	return &Runtime{
		Cache:    cache,
		Registry: registry,
		Engine:   engine,
		Events:   events,
		Log:      log,
	}
}
