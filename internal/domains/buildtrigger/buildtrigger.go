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
Package buildtrigger implements the BuildTrigger resource domain.

The BuildTrigger domain is responsible for managing the lifecycle of
BuildTrigger resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, delegates
processing to the application layer, and records reconciliation
outcomes through conditions and events.
*/

package buildtrigger

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	environmentsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	libbuildtrigger "github.com/ntlaletsi70/blanketops-environments/cache/buildtrigger"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/buildtrigger/application"
	buildtriggerResolution "github.com/ntlaletsi70/blanketops-environments/resolution/buildtrigger"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/buildtrigger"
)

// BuildTriggerDomain implements the BuildTrigger resource domain.
type BuildTriggerDomain struct {
	// buildTriggerMediator manages prerequisite interactions.
	buildTriggerMediator *buildtrigger.Mediator
	// buildTriggerService handles business logic for build operations.
	buildTriggerService *application.BuildTriggerService
	// buildTriggerCache provides generation-scoped, field-level caching for
	// BuildTrigger resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	buildTriggerCache *libbuildtrigger.BuildTriggerCache
	// events handles logging of Kubernetes events.
	events *core.EventRecorder
	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new BuildTriggerDomain instance configured with the necessary dependencies.
func New(mediator *buildtrigger.Mediator, service *application.BuildTriggerService, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *BuildTriggerDomain {
	return &BuildTriggerDomain{
		buildTriggerMediator: mediator,
		buildTriggerService:  service,
		buildTriggerCache:    libbuildtrigger.NewBuildTriggerCache(cache),
		events:               events,
		log:                  log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *BuildTriggerDomain) GVK() schema.GroupVersionKind {
	return environmentsv1alpha1.GroupVersion.WithKind("BuildTrigger")
}

// Handle executes core.Command operations routed by the Engine.
func (d *BuildTriggerDomain) Handle(ctx context.Context, cmd core.Command) error {

	buildtriggerCR, ok := cmd.Obj.(*environmentsv1alpha1.BuildTrigger)
	if !ok || buildtriggerCR == nil {
		return fmt.Errorf("invalid object passed to BuildTriggerDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "buildtrigger", "name", buildtriggerCR.Name, "namespace", buildtriggerCR.Namespace)
	log.Info("handling buildtrigger command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(buildtriggerCR)
	gen := buildtriggerCR.GetGeneration()

	switch cmd.Type {
	case core.CmdCreate, core.CmdUpdate:

		// ------------------------------------------------
		// Stage 0: Resolve trigger contract
		// ------------------------------------------------
		resolved, err := buildtriggerResolution.ResolveBuildTrigger(buildtriggerCR)
		if err != nil {
			d.events.FromError(buildtriggerCR, "BuildTriggerResolveFailed", err)
			log.Error(err, "buildtrigger resolution failed")
			d.events.FromError(buildtriggerCR, "BuildTriggerResolveFailed", err)
			core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerResolved", core.ConditionFalse, "InvalidSpec", err.Error())
			return err
		}

		//------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		//------------------------------------------------
		if cerr := d.buildTriggerCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
		}

		log.Info("buildtrigger resolved successfully")
		d.events.Normal(buildtriggerCR, "BuildTriggerResolved", "BuildTrigger specification resolved successfully")
		core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerResolved", core.ConditionTrue, "Resolved", "BuildTrigger specification resolved successfully")

		// ------------------------------------------------
		// 2. Ensure prerequisites (noop today, but real boundary)
		// ------------------------------------------------
		log.Info("ensuring buildtrigger prerequisites")
		if err := d.buildTriggerMediator.EnsurePrerequisites(ctx, resolved); err != nil {
			log.Error(err, "buildtrigger prerequisites failed")
			d.events.FromError(buildtriggerCR, "BuildTriggerPrerequisitesFailed", err)
			core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerPrerequisitesReady", core.ConditionFalse, "BuildTriggerPrerequisitesFailed", err.Error())
			return err
		}

		log.Info("buildtrigger prerequisites ensured")
		d.events.Normal(buildtriggerCR, "BuildTriggerPrerequisitesReady", "All buildtrigger prerequisites created successfully")
		core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerPrerequisitesReady", core.ConditionTrue, "BuildTriggerPrerequisitesReady", "All buildtrigger prerequisites satisfied")

		// ------------------------------------------------
		// 3. Evaluate trigger intent (DECISION ONLY)
		// ------------------------------------------------
		log.Info("triggering buildtrigger evaluation")
		if err := d.buildTriggerService.Evaluate(ctx, resolved); err != nil {
			log.Error(err, "buildtrigger evaluation failed")
			d.events.FromError(buildtriggerCR, "BuildTriggerEvaluationFailed", err)
			core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerEvaluated", core.ConditionFalse, "BuildTriggerEvaluationFailed", err.Error())
			return err
		}

		// ------------------------------------------------
		// 4. Evaluation completed
		// ------------------------------------------------
		log.Info("buildtrigger evaluation requested")
		d.events.Normal(buildtriggerCR, "BuildTriggerEvaluated", "buildtrigger evaluation has started")
		core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerEvaluated", core.ConditionTrue, "Evaluated", "BuildTrigger evaluated successfully")
		log.Info("buildtrigger domain handling complete")

	case core.CmdDelete:
		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.buildTriggerCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}
		d.events.Info(buildtriggerCR, "BuildTriggerDeleted", "buildtrigger cleanup not implemented yet")
	}

	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

// CanCreate reports whether the supplied object can be processed as a BuildTrigger create operation.
func (d *BuildTriggerDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*environmentsv1alpha1.BuildTrigger)
	return ok
}

// CanUpdate reports whether the supplied update should trigger BuildTrigger reconciliation
// by comparing the specifications of the old and new objects.
func (d *BuildTriggerDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldT, okOld := oldObj.(*environmentsv1alpha1.BuildTrigger)
	newT, okNew := newObj.(*environmentsv1alpha1.BuildTrigger)

	// If either object is not a BuildTrigger, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	return !reflect.DeepEqual(oldT.Spec, newT.Spec)
}

// CanDelete reports whether the supplied object can be processed as a BuildTrigger delete operation.
func (d *BuildTriggerDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*environmentsv1alpha1.BuildTrigger)
	return ok
}
