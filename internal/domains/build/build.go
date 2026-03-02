package build

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	buildv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"

	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/build"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	buildResolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type BuildDomain struct {
	buildMediator *build.Mediator
	BuildService  *application.BuildService
	cache         *core.Cache
	events        *core.EventRecorder
	log           logr.Logger
}

func New(
	buildMediator *build.Mediator,
	buildService *application.BuildService,
	cache *core.Cache,
	events *core.EventRecorder,
	log logr.Logger,
) *BuildDomain {
	return &BuildDomain{
		buildMediator: buildMediator,
		BuildService:  buildService,
		cache:         cache,
		events:        events,
		log:           log,
	}
}

func (d *BuildDomain) GVK() schema.GroupVersionKind {
	return buildv1alpha1.GroupVersion.WithKind("Build")
}

func (d *BuildDomain) Handle(ctx context.Context, cmd core.Command) error {
	buildCR, ok := cmd.Obj.(*buildv1alpha1.Build)
	if !ok || buildCR == nil {
		return fmt.Errorf("invalid object passed to BuildDomain: %T", cmd.Obj)
	}

	d.log.Info("handling build command",
		"type", cmd.Type,
		"name", buildCR.Name,
	)

	// ------------------------------------------------
	// 1. Resolve build contract
	// ------------------------------------------------
	resolved, err := buildResolution.ResolveBuild(buildCR)
	if err != nil {
		d.events.FromError(buildCR, "BuildResolveFailed", err)

		core.SetCondition(
			&buildCR.Status.Conditions,
			"BuildResolved",
			core.ConditionFalse,
			"InvalidSpec",
			err.Error(),
		)

		return err
	}

	core.SetCondition(
		&buildCR.Status.Conditions,
		"BuildResolved",
		core.ConditionTrue,
		"Resolved",
		"Build specification resolved successfully",
	)

	// ------------------------------------------------
	// 2. Ensure prerequisites
	// ------------------------------------------------
	if err := d.buildMediator.EnsurePrerequisites(ctx, resolved); err != nil {
		d.events.FromError(buildCR, "PrerequisitesFailed", err)

		core.SetCondition(
			&buildCR.Status.Conditions,
			"BuildPrerequisitesReady",
			core.ConditionFalse,
			"PrerequisitesFailed",
			err.Error(),
		)

		return err
	}

	core.SetCondition(
		&buildCR.Status.Conditions,
		"BuildPrerequisitesReady",
		core.ConditionTrue,
		"PrerequisitesReady",
		"All prerequisites created successfully",
	)

	// ------------------------------------------------
	// 3. Trigger execution (INTENT ONLY)
	// ------------------------------------------------
	if err := d.BuildService.Reconcile(ctx, resolved); err != nil {
		d.events.FromError(buildCR, "BuildTriggerFailed", err)

		core.SetCondition(
			&buildCR.Status.Conditions,
			"BuildTriggered",
			core.ConditionFalse,
			"TriggerFailed",
			err.Error(),
		)

		return err
	}

	// ------------------------------------------------
	// 4. Execution requested
	// ------------------------------------------------
	core.SetCondition(
		&buildCR.Status.Conditions,
		"BuildTriggered",
		core.ConditionTrue,
		"ExecutionStarted",
		"Build execution has started",
	)

	return nil
}

func (d *BuildDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*buildv1alpha1.Build)
	return ok
}

func (d *BuildDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldB, okOld := oldObj.(*buildv1alpha1.Build)
	newB, okNew := newObj.(*buildv1alpha1.Build)
	if !okOld || !okNew {
		return false
	}

	return !reflect.DeepEqual(oldB.Spec, newB.Spec)
}

func (d *BuildDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*buildv1alpha1.Build)
	return ok
}
