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
Package deployment implements the Deployment resource domain.

The Deployment domain is responsible for managing the lifecycle of
Deployment resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, delegates
processing to the application layer, and records reconciliation
outcomes through conditions and events.
*/

package deployment

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	environmentv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	libdeployment "github.com/ntlaletsi70/blanketops-environments/cache/deployment"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/deployment/application"
	deployapp "github.com/ntlaletsi70/blanketops-environments/pkg/deployment/application"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"
	"github.com/ntlaletsi70/blanketops-environments/resolution/serviceunit"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/deployment"
	deploymediator "github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/deployment"
)

// DeployDomain implements the Deployment resource domain.
type DeployDomain struct {
	// deployMediator manages prerequisite interactions.
	deployMediator *deploymediator.Mediator

	// deployService handles business logic for deployment operations.
	deployService *deployapp.DeploymentService
	// deploymentCache provides generation-scoped, field-level caching for
	// Deployment resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	deploymentCache *libdeployment.DeploymentCache

	// reader provides cached (informer-backed) reads of cluster state,
	// used for cross-CR resolution (e.g. ServiceUnits referenced by a
	// Deployment). Truth comes from here; the projection cache never
	// serves cross-CR reads.
	reader client.Reader
	// events handles logging of Kubernetes events.
	events *core.EventRecorder
	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new DeployDomain instance configured with the necessary dependencies.
func New(deploymentMediator *deployment.Mediator, deploymentService *application.DeploymentService, deploymentCache *libdeployment.DeploymentCache, reader client.Reader, events *core.EventRecorder, log logr.Logger) *DeployDomain {
	return &DeployDomain{
		deployMediator:  deploymentMediator,
		deployService:   deploymentService,
		deploymentCache: deploymentCache,
		reader:          reader,
		events:          events,
		log:             log,
	}
}

// GVK tells the core.Engine which CRD type this domain handles.
func (d *DeployDomain) GVK() schema.GroupVersionKind {
	return environmentv1.GroupVersion.WithKind("Deployment")
}

// Handle executes core.Command operations routed by the Engine.
func (d *DeployDomain) Handle(ctx context.Context, cmd core.Command) error {

	deployCR, ok := cmd.Obj.(*environmentv1.Deployment)
	if !ok || deployCR == nil {
		return fmt.Errorf("invalid object passed to DeployDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "deployment", "name", deployCR.Name, "namespace", deployCR.Namespace)
	d.log.Info("handling deployment command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(deployCR)
	gen := deployCR.GetGeneration()

	switch cmd.Type {
	case core.CmdCreate, core.CmdUpdate:

		//------------------------------------------------
		// Stage 0: Resolve deployment contract
		//------------------------------------------------
		log.Info("resolving deployment contract")
		resolved, err := deploymentResolution.ResolveDeployment(deployCR)
		if err != nil {
			log.Error(err, "deployment resolution failed")
			d.events.FromError(deployCR, "DeploymentResolutionFailed", err)
			core.SetCondition(&deployCR.Status.Conditions, "DeploymentResolved", core.ConditionFalse, "InvalidSpec", err.Error())
			return err
		}

		//------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		//------------------------------------------------
		if cerr := d.deploymentCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
		}

		log.Info("deployment resolved successfully")
		d.events.Normal(deployCR, "DeploymentResolved", "Deployment specification resolved successfully")
		core.SetCondition(&deployCR.Status.Conditions, "DeploymentResolved", core.ConditionTrue, "Resolved", "Deployment specification resolved successfully")

		//------------------------------------------------
		// Stage 2: Ensure prerequisites
		//------------------------------------------------
		log.Info("ensuring deployment prerequisites")
		if err := d.deployMediator.EnsurePrerequisites(ctx, resolved); err != nil {
			log.Error(err, "deployment prerequisites failed")
			d.events.FromError(deployCR, "DeploymentPrerequisitesFailed", err)
			core.SetCondition(&deployCR.Status.Conditions, "DeploymentPrerequisitesReady", core.ConditionFalse, "DeploymentPrerequisitesFailed", err.Error())
			return err
		}

		log.Info("deployment prerequisites ensured")
		d.events.Normal(deployCR, "DeploymentPrerequisitesReady", "All deployment prerequisites created successfully")
		core.SetCondition(&deployCR.Status.Conditions, "DeploymentPrerequisitesReady", core.ConditionTrue, "DeploymentPrerequisitesReady", "All deployment prerequisites satisfied")

		// ------------------------------------------------
		// 3. RESOLVE SERVICE UNITS (AUTHORITATIVE)
		// ------------------------------------------------
		log.Info("triggering resolve serviceunits")
		serviceUnits, err := d.resolveServiceUnits(ctx, resolved)
		if err != nil {
			log.Error(err, "resolve serviceunits failed")
			d.events.FromError(deployCR, "ServiceUnitResolutionFailed", err)
			core.SetCondition(&deployCR.Status.Conditions, "ServiceUnitResolved", core.ConditionFalse, "ServiceUnitResolveFailed", err.Error())
			return err
		}

		log.Info("serviceunit resolved successfully")
		d.events.Normal(deployCR, "ServiceUnitResolved", "ServiceUnit specification resolved successfully")
		core.SetCondition(&deployCR.Status.Conditions, "ServiceUnitResolved", core.ConditionTrue, "Resolved", "ServiceUnit specification resolved successfully")

		// ------------------------------------------------
		// 4. EXECUTE DEPLOYMENT (OPTIONAL)
		// ------------------------------------------------
		log.Info("triggering deployment of serviceunit(s)")
		if d.deployService != nil {
			if err := d.deployService.Reconcile(ctx, resolved, serviceUnits, d.log); err != nil {
				log.Error(err, "deployment of serviceunits failed")
				d.events.FromError(deployCR, "DeploymentFailed", err)
				core.SetCondition(&deployCR.Status.Conditions, "DeploymentFailed", core.ConditionFalse, "DeploymentFailed", err.Error())
				return err
			}
		}

		log.Info("serviceunit resolved successfully")
		d.events.Info(deployCR, "DeploymentSucceeded", "deployment reconciliation completed successfully")
		core.SetCondition(&deployCR.Status.Conditions, "DeploymentSucceeded", core.ConditionTrue, "DeploymentSucceeded", "Deployment trigger completed successfully")

	case core.CmdDelete:
		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.deploymentCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}
		d.events.Info(deployCR, "DeploymentDeleted", "deployment cleanup not implemented yet")
	}

	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

// CanCreate reports whether the supplied object can be processed as a Deploy create operation.
func (d *DeployDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*environmentv1.Deployment)
	return ok
}

// CanUpdate reports whether the supplied update should trigger Deploy reconciliation
// by comparing the specifications of the old and new objects.
func (d *DeployDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldDep, okOld := oldObj.(*environmentv1.Deployment)
	newDep, okNew := newObj.(*environmentv1.Deployment)

	// If either object is not a Deployment, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldDep.Spec, newDep.Spec)
}

// CanDelete reports whether the supplied object can be processed as a Deploy delete operation.
func (d *DeployDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*environmentv1.Deployment)
	return ok
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func (d *DeployDomain) resolveServiceUnits(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) ([]serviceunit.ResolvedServiceUnit, error) {
	out := make([]serviceunit.ResolvedServiceUnit, 0, len(resolved.Spec.ServiceUnits))
	for _, name := range resolved.Spec.ServiceUnits {
		var suCR environmentv1.ServiceUnit
		if err := d.reader.Get(
			ctx,
			client.ObjectKey{
				Name:      name,
				Namespace: resolved.Deployment.Namespace,
			},
			&suCR,
		); err != nil {
			return nil, fmt.Errorf("resolving service unit %q: %w", name, err)
		}
		su, err := serviceunit.ResolveServiceUnit(&suCR)
		if err != nil {
			return nil, fmt.Errorf("resolving service unit %q: %w", name, err)
		}
		out = append(out, *su)
	}
	return out, nil
}
