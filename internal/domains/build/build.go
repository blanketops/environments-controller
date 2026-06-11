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
Package build implements the Build resource domain.

The Build domain is responsible for managing the lifecycle of
Build resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, delegates
processing to the application layer, and records reconciliation
outcomes through conditions and events.
*/

package build

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	environmentsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	libbuild "github.com/ntlaletsi70/blanketops-environments/cache/build"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	buildResolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/build"
)

// BuildDomain implements the Build resource domain logic.
type BuildDomain struct {
	// buildMediator manages prerequisite interactions.
	buildMediator *build.Mediator
	// buildService handles business logic for build operations.
	buildService *application.BuildService
	// buildCache provides generation-scoped, field-level caching for
	// Build resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	buildCache *libbuild.BuildCache
	// events handles logging of Kubernetes events.
	events *core.EventRecorder
	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new BuildDomain instance configured with the necessary dependencies.
func New(buildMediator *build.Mediator, buildService *application.BuildService, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *BuildDomain {
	return &BuildDomain{
		buildMediator: buildMediator,
		buildService:  buildService,
		buildCache:    libbuild.NewBuildCache(cache),
		events:        events,
		log:           log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *BuildDomain) GVK() schema.GroupVersionKind {
	return environmentsv1alpha1.GroupVersion.WithKind("Build")
}

// Handle executes core.Command operations routed by the Engine.
func (d *BuildDomain) Handle(ctx context.Context, cmd core.Command) error {

	buildCR, ok := cmd.Obj.(*environmentsv1alpha1.Build)
	if !ok || buildCR == nil {
		return fmt.Errorf("invalid object passed to BuildDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "build", "name", buildCR.Name, "namespace", buildCR.Namespace)
	log.Info("handling build command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(buildCR)
	gen := buildCR.GetGeneration()

	switch cmd.Type {
	case core.CmdCreate, core.CmdUpdate:

		//------------------------------------------------
		// Stage 0: Resolve build contract
		//------------------------------------------------
		log.Info("resolving build contract")
		resolved, err := buildResolution.ResolveBuild(buildCR)
		if err != nil {
			log.Error(err, "build resolution failed")
			d.events.FromError(buildCR, "BuildResolveFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildResolved", core.ConditionFalse, "InvalidSpec", err.Error())
			return err
		}

		//------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		//------------------------------------------------
		if cerr := d.buildCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
		}

		log.Info("build resolved successfully")
		d.events.Normal(buildCR, "BuildResolved", "Build specification resolved successfully")
		core.SetCondition(&buildCR.Status.Conditions, "BuildResolved", core.ConditionTrue, "Resolved", "Build specification resolved successfully")

		//------------------------------------------------
		// Stage 2: Ensure prerequisites
		//------------------------------------------------
		log.Info("ensuring build prerequisites")
		if err := d.buildMediator.EnsurePrerequisites(ctx, resolved); err != nil {
			log.Error(err, "build prerequisites failed")
			d.events.FromError(buildCR, "BuildPrerequisitesFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildPrerequisitesReady", core.ConditionFalse, "BuildPrerequisitesFailed", err.Error())
			return err
		}

		log.Info("build prerequisites ensured")
		d.events.Normal(buildCR, "BuildPrerequisitesReady", "all build prerequisites created successfully")
		core.SetCondition(&buildCR.Status.Conditions, "BuildPrerequisitesReady", core.ConditionTrue, "BuildPrerequisitesReady", "all build prerequisites satisfied")

		//------------------------------------------------
		// Stage 3: Trigger execution (intent only)
		//------------------------------------------------
		log.Info("triggering build execution")
		if err := d.buildService.Reconcile(ctx, resolved); err != nil {
			log.Error(err, "build triggering failed")
			d.events.FromError(buildCR, "BuildServiceReconFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildTriggered", core.ConditionFalse, "TriggerFailed", err.Error())
			return err
		}

		// ------------------------------------------------
		// 4. Build Execution completed
		// ------------------------------------------------
		log.Info("build execution requested")
		d.events.Normal(buildCR, "BuildTriggered", "Build execution has started")
		core.SetCondition(&buildCR.Status.Conditions, "BuildTriggered", core.ConditionTrue, "ExecutionStarted", "Build execution has started")
		log.Info("build domain handling complete")

	case core.CmdDelete:
		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.buildCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}
		d.events.Info(buildCR, "BuildDeleted", "build cleanup not implemented yet")
	}
	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

// CanCreate reports whether the supplied object can be processed as a Build create operation.
func (d *BuildDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*environmentsv1alpha1.Build)
	return ok
}

// CanUpdate reports whether the supplied update should trigger Build reconciliation
// by comparing the specifications of the old and new objects.
func (d *BuildDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldB, okOld := oldObj.(*environmentsv1alpha1.Build)
	newB, okNew := newObj.(*environmentsv1alpha1.Build)

	// If either object is not a Build, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldB.Spec, newB.Spec)
}

// CanDelete reports whether the supplied object can be processed as a Build delete operation.
func (d *BuildDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*environmentsv1alpha1.Build)
	return ok
}
