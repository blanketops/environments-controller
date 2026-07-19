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

package runtime

import (
	"github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/engine"
	"github.com/blanketops/environments/core/events"
	"github.com/blanketops/environments/core/registry"
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

type Runtime struct {
	Cache    *cache.Cache
	Events   *events.EventRecorder
	Registry *registry.Registry
	Engine   *engine.Engine
	Log      logr.Logger
}

func New(mgr ctrl.Manager) *Runtime {

	log := ctrl.Log.WithName("environments-runtime")
	cache := cache.NewCache(mgr, nil)
	registry := registry.NewRegistry()

	engine := engine.NewEngine(registry, ctrl.Log.WithName("environments-engine"))
	events := events.NewEventRecorder(mgr.GetEventRecorder("environments-runtime"))

	return &Runtime{
		Cache:    cache,
		Registry: registry,
		Engine:   engine,
		Events:   events,
		Log:      log,
	}
}
