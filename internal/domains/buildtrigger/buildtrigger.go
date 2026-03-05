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

type BuildTriggerDomain struct {
	mediator *buildtrigger.Mediator
	service  *application.BuildTriggerService
	cache    *core.Cache
	events   *core.EventRecorder
	log      logr.Logger
}

func New(
	mediator *buildtrigger.Mediator,
	service *application.BuildTriggerService,
	cache *core.Cache,
	events *core.EventRecorder,
	log logr.Logger,
) *BuildTriggerDomain {
	return &BuildTriggerDomain{
		mediator: mediator,
		service:  service,
		cache:    cache,
		events:   events,
		log:      log,
	}
}

func (d *BuildTriggerDomain) GVK() schema.GroupVersionKind {
	return environmentsv1alpha1.GroupVersion.WithKind("BuildTrigger")
}

func (d *BuildTriggerDomain) Handle(ctx context.Context, cmd core.Command) error {
	triggerCR, ok := cmd.Obj.(*environmentsv1alpha1.BuildTrigger)
	if !ok || triggerCR == nil {
		return fmt.Errorf("invalid object passed to BuildTriggerDomain: %T", cmd.Obj)
	}

	d.log.Info("handling buildtrigger command",
		"type", cmd.Type,
		"name", triggerCR.Name,
	)

	// ------------------------------------------------
	// 1. Resolve trigger contract
	// ------------------------------------------------
	resolved, err := buildtriggerResolution.ResolveBuildTrigger(triggerCR)
	if err != nil {
		d.events.FromError(triggerCR, "BuildTriggerResolveFailed", "ResolveContract", err)

		core.SetCondition(
			&triggerCR.Status.Conditions,
			"BuildTriggerResolved",
			core.ConditionFalse,
			"InvalidSpec",
			err.Error(),
		)

		return err
	}

	core.SetCondition(
		&triggerCR.Status.Conditions,
		"BuildTriggerResolved",
		core.ConditionTrue,
		"Resolved",
		"BuildTrigger specification resolved successfully",
	)

	// ------------------------------------------------
	// 2. Ensure prerequisites (noop today, but real boundary)
	// ------------------------------------------------
	if err := d.mediator.EnsurePrerequisites(ctx, resolved); err != nil {
		d.events.FromError(triggerCR, "BuildTriggerPrerequisitesFailed", "BuildTriggerMediator", err)

		core.SetCondition(
			&triggerCR.Status.Conditions,
			"BuildTriggerPrerequisitesReady",
			core.ConditionFalse,
			"PrerequisitesFailed",
			err.Error(),
		)

		return err
	}

	core.SetCondition(
		&triggerCR.Status.Conditions,
		"BuildTriggerPrerequisitesReady",
		core.ConditionTrue,
		"PrerequisitesReady",
		"All BuildTrigger prerequisites satisfied",
	)

	// ------------------------------------------------
	// 3. Evaluate trigger intent (DECISION ONLY)
	// ------------------------------------------------
	if err := d.service.Evaluate(ctx, resolved); err != nil {
		d.events.FromError(triggerCR, "BuildTriggerEvaluationFailed", "BuildServiceEvaluation", err)

		core.SetCondition(
			&triggerCR.Status.Conditions,
			"BuildTriggerEvaluated",
			core.ConditionFalse,
			"EvaluationFailed",
			err.Error(),
		)

		return err
	}

	// ------------------------------------------------
	// 4. Evaluation completed
	// ------------------------------------------------
	core.SetCondition(
		&triggerCR.Status.Conditions,
		"BuildTriggerEvaluated",
		core.ConditionTrue,
		"Evaluated",
		"BuildTrigger evaluated successfully",
	)

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
