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

/*
Package runtime bundles the shared CQRS infrastructure every domain and
mediator in this controller depends on: the field cache, event recorder,
Domain registry, and command-dispatch Engine (all from
github.com/blanketops/environments/core). New constructs exactly one
Runtime per controller manager at startup; domains register themselves
against its Registry, and controllers route observed events through its
Engine rather than handling reconciliation logic directly.
*/
package runtime

import (
	"github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/engine"
	"github.com/blanketops/environments/core/events"
	"github.com/blanketops/environments/core/registry"
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Runtime bundles the shared infrastructure every domain and mediator
// depends on: the field cache, event recorder, domain registry, and
// command-dispatch engine.
type Runtime struct {
	Cache    *cache.Cache
	Events   *events.EventRecorder
	Registry *registry.Registry
	Engine   *engine.Engine
	Log      logr.Logger
}

// New constructs a Runtime from the controller manager, wiring up a fresh
// Cache, Registry, Engine, and EventRecorder.
func New(mgr ctrl.Manager) *Runtime {

	log := ctrl.Log.WithName("environments-runtime")
	objCache := cache.NewCache(mgr, nil)
	reg := registry.NewRegistry()

	eng := engine.NewEngine(reg, ctrl.Log.WithName("environments-engine"))
	eventRecorder := events.NewEventRecorder(mgr.GetEventRecorder("environments-runtime"))

	return &Runtime{
		Cache:    objCache,
		Registry: reg,
		Engine:   eng,
		Events:   eventRecorder,
		Log:      log,
	}
}
