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
Package packages implements the Package resource domain.

The Package domain is responsible for managing the lifecycle of
Package resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, delegates
processing to the application layer, and records reconciliation
outcomes through conditions and events.
*/
package packages

import (
	"context"
	"fmt"
	"reflect"

	environmentv1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	libpackages "github.com/BlanketOps/environments/cache/packages"
	"github.com/BlanketOps/environments/core"
	pkgApplication "github.com/BlanketOps/environments/pkg/apis/packages/application"
	pkgIntent "github.com/BlanketOps/environments/pkg/apis/packages/intent"
	pkgResolution "github.com/BlanketOps/environments/resolution/packages"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pkgMediator "github.com/BlanketOps/environments-controller/internal/mediators/packages"
)

// PackageDomain implements the Package resource domain logic.
type PackageDomain struct {
	// packageMediator manages prerequisite interactions.
	packageMediator *pkgMediator.Mediator

	// packageService handles business logic for package operations.
	packageService *pkgApplication.PackageService

	// packageCache provides generation-scoped, field-level caching for
	// Package resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	packageCache *libpackages.PackageCache

	// events handles logging of Kubernetes events.
	events *core.EventRecorder

	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new PackageDomain instance configured with the necessary dependencies.
func New(packageMediator *pkgMediator.Mediator, packageService *pkgApplication.PackageService, cache *core.Cache, events *core.EventRecorder, log logr.Logger,
) *PackageDomain {
	return &PackageDomain{
		packageMediator: packageMediator,
		packageService:  packageService,
		packageCache:    libpackages.NewPackageCache(cache),
		events:          events,
		log:             log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *PackageDomain) GVK() schema.GroupVersionKind {
	return environmentv1.GroupVersion.WithKind("Package")
}

// Handle executes core.Command operations routed by the Engine.
func (d *PackageDomain) Handle(ctx context.Context, cmd core.Command) error {

	packageCR, ok := cmd.Obj.(*environmentv1.Package)
	if !ok || packageCR == nil {
		return fmt.Errorf("invalid object passed to PackageDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "package", "name", packageCR.Name, "namespace", packageCR.Namespace)
	log.Info("handling package command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(packageCR)
	gen := packageCR.GetGeneration()

	switch cmd.Type {
	case core.CmdCreate, core.CmdUpdate:

		// ------------------------------------------------
		// Stage 0: Resolve package contract
		// ------------------------------------------------
		log.Info("resolving package contract")
		resolved, err := pkgResolution.ResolvePackage(packageCR)
		if err != nil {
			log.Error(err, "package resolution failed")
			d.events.FromError(packageCR, "PackageResolveFailed", err)
			core.SetCondition(&packageCR.Status.Conditions, "PackageResolved", core.ConditionFalse, "InvalidSpec", err.Error())
			return err
		}

		// ------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		// ------------------------------------------------
		if cerr := d.packageCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
			d.events.FromError(packageCR, "PackageCacheFailed", cerr)
			core.SetCondition(&packageCR.Status.Conditions, "PackageCacheFailed", core.ConditionFalse, "resolved projection publish incomplete", cerr.Error())
		}

		log.Info("package resolved successfully")
		d.events.Normal(packageCR, "PackageResolved", "Package specification resolved successfully")
		core.SetCondition(&packageCR.Status.Conditions, "PackageResolved", core.ConditionTrue, "Resolved", "Package specification resolved successfully")

		log.Info("package cached successfully")
		d.events.Normal(packageCR, "PackageCached", "Package specification cached successfully")
		core.SetCondition(&packageCR.Status.Conditions, "PackageCached", core.ConditionTrue, "PackageSpecCached", "Package specification cached successfully")

		// ------------------------------------------------
		// 2. Ensure prerequisites (secrets, repos, identity)
		// ------------------------------------------------
		log.Info("creating package prerequisites")
		if err := d.packageMediator.EnsurePrerequisites(ctx, resolved); err != nil {
			log.Error(err, "package prerequisites failed")
			d.events.FromError(packageCR, "PackagePrerequisitesCreateFailed", err)
			core.SetCondition(&packageCR.Status.Conditions, "PackagePrerequisitesCreateFailed", core.ConditionFalse, "package prerequisites failed, internal error", err.Error())
			return err
		}

		log.Info("package prerequisites created")
		d.events.Normal(packageCR, "PackagePrerequisitesCreated", "All package prerequisites created successfully")
		core.SetCondition(&packageCR.Status.Conditions, "PackagePrerequisitesCreated", core.ConditionTrue, "PackagePrerequisitesReady", "All package prerequisites satisfied")

		// ------------------------------------------------------------------
		// 3. Build execution intent (INTENT ONLY)
		// ------------------------------------------------------------------
		log.Info("build package intent execution")
		intent, err := pkgIntent.BuildPackageIntent(resolved)
		if err != nil {
			log.Error(err, "package intent build execution failed")
			d.events.FromError(packageCR, "PackageIntentBuildExecutionFailed", err)
			core.SetCondition(&packageCR.Status.Conditions, "PackageIntentBuilt", core.ConditionFalse, "PackageIntentBuildFailed", err.Error())
			return err
		}

		log.Info("package intent execution completed")
		d.events.Normal(packageCR, "PackageIntentBuildComplete", "Package intent built successfully")
		core.SetCondition(&packageCR.Status.Conditions, "PackageIntentBuilt", core.ConditionTrue, "PackageBuildIntentReady", "Package intent build execution completed successfully")

		// ----------------------------------------------------------------
		// 4. Trigger execution (authoritative service)
		// ----------------------------------------------------------------
		log.Info("triggering package execution")
		if err := d.packageService.Reconcile(ctx, resolved, intent); err != nil {
			log.Error(err, "package triggering failed")
			d.events.FromError(packageCR, "PackageTriggerFailed", err)
			core.SetCondition(&packageCR.Status.Conditions, "PackageTriggered", core.ConditionFalse, "TriggerFailed", err.Error())
			return err
		}

		// -----------------------------------------------------------------
		// 5. Execution requested (NOT completed)
		// -----------------------------------------------------------------
		core.SetCondition(&packageCR.Status.Conditions, "PackageTriggered", core.ConditionTrue, "ExecutionRequested", "Package execution has been requested")
		d.events.Normal(packageCR, "PackageTriggered", "Package execution has been requested")

	case core.CmdDelete:
		// --------------------------------------------------------
		// Real teardown, gated by finalizer at the controller level.
		// Handle() must return nil ONLY if it is safe for the
		// controller to remove the finalizer and let K8s finish
		// deleting the object. Any error here keeps the finalizer
		// in place and the controller will retry on next reconcile.
		// --------------------------------------------------------
		log.Info("package teardown requested")

		resolved, err := pkgResolution.ResolvePackage(packageCR)
		if err != nil {
			log.Error(err, "resolution failed during teardown")
			d.events.FromError(packageCR, "PackageTeardownResolveFailed", err)
			core.SetCondition(&packageCR.Status.Conditions, "PackageDeleted", core.ConditionFalse, "PackageTeardownResolveFailed", err.Error())
			return err
		}

		// Tear down prerequisites the mediator created (secrets, SAs, RBAC).
		if err := d.packageMediator.CleanupPrerequisites(ctx, resolved); err != nil {
			log.Error(err, "prerequisites cleanup failed")
			d.events.FromError(packageCR, "PackagePrerequisitesCleanupFailed", err)
			core.SetCondition(&packageCR.Status.Conditions, "PackageDeleted", core.ConditionFalse, "PackagePrerequisitesCleanupFailed", err.Error())
			return err
		}

		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.packageCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}

		log.Info("package teardown complete")
		d.events.Normal(packageCR, "PackageDeleted", "package and owned resources cleaned up successfully")
		core.SetCondition(&packageCR.Status.Conditions, "PackageDeleted", core.ConditionTrue, "PackageCleanupComplete", "package and owned resources cleaned up successfully")
	}
	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

// CanCreate reports whether the supplied object can be processed as a Build create operation.
func (d *PackageDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*environmentv1.Package)
	return ok
}

// CanUpdate reports whether the supplied update should trigger Build reconciliation
// by comparing the specifications of the old and new objects.
func (d *PackageDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldPkg, okOld := oldObj.(*environmentv1.Package)
	newPkg, okNew := newObj.(*environmentv1.Package)

	// If either object is not a Package, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldPkg.Spec, newPkg.Spec)
}

// CanDelete reports whether the supplied object can be processed as a Package delete operation.
func (d *PackageDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*environmentv1.Package)
	return ok
}
