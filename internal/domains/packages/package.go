package packages

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	environmentv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"

	pkgMediator "github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/packages"
	pkgApplication "github.com/ntlaletsi70/blanketops-environments/pkg/packages/application"
	pkgIntent "github.com/ntlaletsi70/blanketops-environments/pkg/packages/intent"
	pkgResolution "github.com/ntlaletsi70/blanketops-environments/resolution/packages"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// PackageDomain handles Build CRs.
// This represents a FACT INGESTION boundary.
type PackageDomain struct {
	packageMediator *pkgMediator.Mediator
	packageService  *pkgApplication.PackageService
	cache           *core.Cache
	events          *core.EventRecorder
	log             logr.Logger
}

func New(packageMediator *pkgMediator.Mediator, packageService *pkgApplication.PackageService, cache *core.Cache, events *core.EventRecorder, log logr.Logger,
) *PackageDomain {
	return &PackageDomain{
		packageMediator: packageMediator,
		packageService:  packageService,
		cache:           cache,
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

	//------------------------------------------------
	// Stage 1: Resolve package contract
	//------------------------------------------------

	log.Info("resolving package contract")
	resolved, err := pkgResolution.ResolvePackage(packageCR)

	if err != nil {

		log.Error(err, "package resolution failed")
		d.events.FromError(packageCR, "PackageResolveFailed", err)
		core.SetCondition(&packageCR.Status.Conditions, "PackageResolved", core.ConditionFalse, "InvalidSpec", err.Error())

		return err
	}

	log.Info("package resolved successfully")
	d.events.Normal(packageCR, "PackageResolved", "Package specification resolved successfully")
	core.SetCondition(&packageCR.Status.Conditions, "PackageResolved", core.ConditionTrue, "Resolved", "Package specification resolved successfully")

	// ------------------------------------------------
	// 2. Ensure prerequisites (secrets, repos, identity)
	// ------------------------------------------------

	log.Info("ensuring package prerequisites")

	if err := d.packageMediator.EnsurePrerequisites(ctx, resolved); err != nil {

		log.Error(err, "package prerequisites failed")
		d.events.FromError(packageCR, "PackagePrerequisitesFailed", err)
		core.SetCondition(&packageCR.Status.Conditions, "PackagePrerequisitesReady", core.ConditionFalse, "PackagePrerequisitesFailed", err.Error())

		return err
	}

	log.Info("package prerequisites ensured")
	d.events.Normal(packageCR, "PackagePrerequisitesReady", "All package prerequisites created successfully")
	core.SetCondition(&packageCR.Status.Conditions, "PackagePrerequisitesReady", core.ConditionTrue, "PackagePrerequisitesReady", "All package prerequisites created successfully")

	//------------------------------------------------------------------
	// 3. Build execution intent (INTENT ONLY)
	//------------------------------------------------------------------

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

	//-----------------------------------------------------------------
	// 5. Execution requested (NOT completed)
	//-----------------------------------------------------------------
	core.SetCondition(&packageCR.Status.Conditions, "PackageTriggered", core.ConditionTrue, "ExecutionRequested", "Package execution has been requested")

	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

func (d *PackageDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*environmentv1.Package)
	return ok
}

func (d *PackageDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldPkg, okOld := oldObj.(*environmentv1.Package)
	newPkg, okNew := newObj.(*environmentv1.Package)
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldPkg.Spec, newPkg.Spec)
}

func (d *PackageDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*environmentv1.Package)
	return ok
}
