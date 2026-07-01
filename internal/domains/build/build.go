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

	environmentsv1alpha1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	"github.com/go-logr/logr"
	libbuild "github.com/ntlaletsi70/blanketops-environments/cache/build"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/build/application"
	buildResolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/build"
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

		// ------------------------------------------------
		// Stage 0: Resolve build contract
		// ------------------------------------------------
		log.Info("resolving build contract")
		resolved, err := buildResolution.ResolveBuild(buildCR)
		if err != nil {
			log.Error(err, "build resolution failed")
			d.events.FromError(buildCR, "BuildResolveFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildResolveFailed", core.ConditionFalse, "BuildResolve", err.Error())
			return err
		}

		// ------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		// ------------------------------------------------
		if cerr := d.buildCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
			d.events.FromError(buildCR, "BuildCacheFailed", cerr)
			core.SetCondition(&buildCR.Status.Conditions, "BuildCacheFailed", core.ConditionFalse, "resolved projection publish incomplete", cerr.Error())
		}

		log.Info("build resolved successfully")
		d.events.Normal(buildCR, "BuildResolve", "Build specification resolved successfully")
		core.SetCondition(&buildCR.Status.Conditions, "BuildResolved", core.ConditionTrue, "BuildSpecResolved", "Build specification resolved successfully")

		log.Info("build cached successfully")
		d.events.Normal(buildCR, "BuildCache", "Build specification cached successfully")
		core.SetCondition(&buildCR.Status.Conditions, "BuildCached", core.ConditionTrue, "BuildSpecCached", "Build specification cached successfully")

		// ------------------------------------------------
		// Stage 2: Ensure prerequisites
		// ------------------------------------------------
		log.Info("create build prerequisites")
		if err := d.buildMediator.EnsurePrerequisites(ctx, resolved); err != nil {
			log.Error(err, "build prerequisites failed")
			d.events.FromError(buildCR, "BuildPrerequisitesCreateFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildPrerequisitesCreateFailed", core.ConditionFalse, "build prerequisites failed, internal error", err.Error())
			return err
		}

		log.Info("build prerequisites created")
		d.events.Normal(buildCR, "BuildPrerequisitesCreate", "All build prerequisites created successfully")
		core.SetCondition(&buildCR.Status.Conditions, "BuildPrerequisitesCreated", core.ConditionTrue, "BuildPrerequisitesReady", "All build prerequisites satisfied")

		// ------------------------------------------------
		// Stage 3: Start build (intent only)
		// ------------------------------------------------
		log.Info("starting build run")
		if err := d.buildService.Reconcile(ctx, resolved); err != nil {
			log.Error(err, "build run failed")
			d.events.FromError(buildCR, "BuildFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildStart", core.ConditionFalse, "starting build run, internal error", err.Error())
			return err
		}

		// ------------------------------------------------
		// 4. Build Execution started
		// ------------------------------------------------
		log.Info("build run started")
		d.events.Normal(buildCR, "BuildRunStarted", "Build run has started")
		core.SetCondition(&buildCR.Status.Conditions, "BuildStart", core.ConditionTrue, "BuildRunStarted", "Build run has started")
		log.Info("build domain handling complete")

	case core.CmdDelete:
		// --------------------------------------------------------
		// Real teardown, gated by finalizer at the controller level.
		// Handle() must return nil ONLY if it is safe for the
		// controller to remove the finalizer and let K8s finish
		// deleting the object. Any error here keeps the finalizer
		// in place and the controller will retry on next reconcile.
		// --------------------------------------------------------
		log.Info("build teardown requested")

		resolved, err := buildResolution.ResolveBuild(buildCR)
		if err != nil {
			log.Error(err, "resolution failed during teardown")
			d.events.FromError(buildCR, "BuildTeardownResolveFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildDeleted", core.ConditionFalse, "BuildTeardownResolveFailed", err.Error())
			return err
		}

		// Tear down owned external resources (BuildRun, etc).
		if err := d.buildService.Teardown(ctx, resolved); err != nil {
			log.Error(err, "build teardown failed")
			d.events.FromError(buildCR, "BuildTeardownFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildDeleted", core.ConditionFalse, "BuildTeardownFailed", err.Error())
			return err
		}

		// Tear down prerequisites the mediator created (secrets, SAs, RBAC).
		if err := d.buildMediator.CleanupPrerequisites(ctx, resolved); err != nil {
			log.Error(err, "prerequisites cleanup failed")
			d.events.FromError(buildCR, "BuildPrerequisitesCleanupFailed", err)
			core.SetCondition(&buildCR.Status.Conditions, "BuildDeleted", core.ConditionFalse, "BuildPrerequisitesCleanupFailed", err.Error())
			return err
		}

		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.buildCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}

		log.Info("build teardown complete")
		d.events.Normal(buildCR, "BuildDeleted", "build and owned resources cleaned up successfully")
		core.SetCondition(&buildCR.Status.Conditions, "BuildDeleted", core.ConditionTrue, "BuildCleanupComplete", "build and owned resources cleaned up successfully")
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
