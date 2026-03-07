/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package deploy

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"
	"github.com/ntlaletsi70/blanketops-environments/resolution/serviceunit"

	environmentv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	deploymediator "github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/deployment"
	"github.com/ntlaletsi70/blanketops-environments-mvp/core"
	deployapp "github.com/ntlaletsi70/blanketops-environments/pkg/deployment/application"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeploymentDomain handles Build CRs.
// This represents a FACT INGESTION boundary.
type DeployDomain struct {
	deployMediator *deploymediator.Mediator
	deployService  *deployapp.DeploymentService // optional, nil-safe

	cache  *core.Cache
	events *core.EventRecorder
	log    logr.Logger
}

// New constructs a new DeployDomain instance.
func New(deployMediator *deploymediator.Mediator, deployService *deployapp.DeploymentService, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *DeployDomain {
	return &DeployDomain{
		deployMediator: deployMediator,
		deployService:  deployService,
		cache:          cache,
		events:         events,
		log:            log,
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
		return fmt.Errorf("invalid object passed to DeploymentDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "deployment", "name", deployCR.Name, "namespace", deployCR.Namespace)
	d.log.Info("handling deployment command", "type", cmd.Type)

	switch cmd.Type {
	case core.CmdCreate, core.CmdUpdate:

		//------------------------------------------------
		// Stage 1: Resolve deployment contract
		//------------------------------------------------

		log.Info("resolving deployment contract")
		resolved, err := deploymentResolution.ResolveDeployment(deployCR)

		if err != nil {

			log.Error(err, "deployment resolution failed")
			d.events.FromError(deployCR, "DeploymentResolutionFailed", err)
			core.SetCondition(&deployCR.Status.Conditions, "DeploymentResolved", core.ConditionFalse, "InvalidSpec", err.Error())

			return err
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
			if err := d.deployService.Reconcile(ctx, resolved, serviceUnits); err != nil {

				log.Error(err, "deployment of serviceunits failed")
				d.events.FromError(deployCR, "DeploymentFailed", err)
				core.SetCondition(&deployCR.Status.Conditions, "DeploymentFailed", core.ConditionFalse, "DeploymentFailed", err.Error())

				return err
			}
		}

		log.Info("serviceunit resolved successfully")
		d.events.Info(deployCR, "DeploymentSucceeded", "Reconcile", "deployment reconciliation completed successfully")
		core.SetCondition(&deployCR.Status.Conditions, "DeploymentSucceeded", core.ConditionTrue, "DeploymentSucceeded", "Deployment trigger completed successfully")

	case core.CmdDelete:
		d.events.Info(deployCR, "DeploymentDeleted", "deployment cleanup not implemented yet")
	}

	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

func (d *DeployDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*environmentv1.Deployment)
	return ok
}

func (d *DeployDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldDep, okOld := oldObj.(*environmentv1.Deployment)
	newDep, okNew := newObj.(*environmentv1.Deployment)
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldDep.Spec, newDep.Spec)
}

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
		if err := d.cache.Reader.Get(
			ctx,
			client.ObjectKey{
				Name:      name,
				Namespace: resolved.Deployment.Namespace,
			},
			&suCR,
		); err != nil {
			return nil, err
		}

		su, err := serviceunit.ResolveServiceUnit(&suCR)
		if err != nil {
			return nil, err
		}

		out = append(out, *su)
	}

	return out, nil
}
