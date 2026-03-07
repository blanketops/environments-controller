package buildtrigger

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"

	environmentsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/buildtrigger"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/buildtrigger/application"
	buildtriggerResolution "github.com/ntlaletsi70/blanketops-environments/resolution/buildtrigger"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BuildTriggerDomain handles Build CRs.
// This represents a FACT INGESTION boundary.
type BuildTriggerDomain struct {
	mediator *buildtrigger.Mediator
	service  *application.BuildTriggerService
	cache    *core.Cache
	events   *core.EventRecorder
	log      logr.Logger
}

func New(mediator *buildtrigger.Mediator, service *application.BuildTriggerService, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *BuildTriggerDomain {
	return &BuildTriggerDomain{
		mediator: mediator,
		service:  service,
		cache:    cache,
		events:   events,
		log:      log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *BuildTriggerDomain) GVK() schema.GroupVersionKind {
	return environmentsv1alpha1.GroupVersion.WithKind("BuildTrigger")
}

func (d *BuildTriggerDomain) Handle(ctx context.Context, cmd core.Command) error {

	buildtriggerCR, ok := cmd.Obj.(*environmentsv1alpha1.BuildTrigger)
	if !ok || buildtriggerCR == nil {
		return fmt.Errorf("invalid object passed to BuildTriggerDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "buildtrigger", "name", buildtriggerCR.Name, "namespace", buildtriggerCR.Namespace)
	log.Info("handling buildtrigger command", "type", cmd.Type)

	// ------------------------------------------------
	// 1. Resolve trigger contract
	// ------------------------------------------------
	resolved, err := buildtriggerResolution.ResolveBuildTrigger(buildtriggerCR)
	if err != nil {

		d.events.FromError(buildtriggerCR, "BuildTriggerResolveFailed", err)

		log.Error(err, "buildtrigger resolution failed")
		d.events.FromError(buildtriggerCR, "BuildTriggerResolveFailed", err)
		core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerResolved", core.ConditionFalse, "InvalidSpec", err.Error())

		return err
	}

	log.Info("buildtrigger resolved successfully")
	d.events.Normal(buildtriggerCR, "BuildTriggerResolved", "BuildTrigger specification resolved successfully")
	core.SetCondition(&buildtriggerCR.Status.Conditions, "BuildTriggerResolved", core.ConditionTrue, "Resolved", "BuildTrigger specification resolved successfully")

	// ------------------------------------------------
	// 2. Ensure prerequisites (noop today, but real boundary)
	// ------------------------------------------------

	log.Info("ensuring buildtrigger prerequisites")

	if err := d.mediator.EnsurePrerequisites(ctx, resolved); err != nil {

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

	if err := d.service.Evaluate(ctx, resolved); err != nil {

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

	return nil
}

func (d *BuildTriggerDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*environmentsv1alpha1.BuildTrigger)
	return ok
}

func (d *BuildTriggerDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldT, okOld := oldObj.(*environmentsv1alpha1.BuildTrigger)
	newT, okNew := newObj.(*environmentsv1alpha1.BuildTrigger)
	if !okOld || !okNew {
		return false
	}

	return !reflect.DeepEqual(oldT.Spec, newT.Spec)
}

func (d *BuildTriggerDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*environmentsv1alpha1.BuildTrigger)
	return ok
}
