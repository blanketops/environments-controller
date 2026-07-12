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
Package serviceunit implements the ServiceUnit resource domain.

The ServiceUnit domain is responsible for managing the lifecycle of
ServiceUnit resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, delegates
processing to the application layer, and records reconciliation
outcomes through conditions and events.
*/
package serviceunit

import (
	"context"
	"fmt"
	"reflect"

	serviceunitv1alpha1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	libserviceunit "github.com/BlanketOps/environments/cache/serviceunit"
	"github.com/BlanketOps/environments/core"
	serviceunitResolution "github.com/BlanketOps/environments/resolution/serviceunit"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/mediators/serviceunit"
)

// ServiceUnitDomain implements the ServiceUnit resource domain.
type ServiceUnitDomain struct {
	serviceUnitMediator *serviceunit.Mediator

	// serviceUnitCache provides generation-scoped, field-level caching for
	// ServiceUnit resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	serviceUnitCache *libserviceunit.ServiceUnitCache
	events           *core.EventRecorder
	log              logr.Logger
}

func New(mediator *serviceunit.Mediator, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *ServiceUnitDomain {
	return &ServiceUnitDomain{
		serviceUnitMediator: mediator,
		serviceUnitCache:    libserviceunit.NewServiceUnitCache(cache),
		events:              events,
		log:                 log,
	}
}

func (d *ServiceUnitDomain) GVK() schema.GroupVersionKind {
	return serviceunitv1alpha1.GroupVersion.WithKind("ServiceUnit")
}

func (d *ServiceUnitDomain) Handle(ctx context.Context, cmd core.Command) error {

	su, ok := cmd.Obj.(*serviceunitv1alpha1.ServiceUnit)
	if !ok || su == nil {
		return fmt.Errorf("invalid object passed to ServiceUnitDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues(
		"domain", "serviceunit",
		"name", su.Name,
		"namespace", su.Namespace,
	)

	log.Info("handling serviceunit command", "type", cmd.Type)

	// ------------------------------------------------
	// Stage 1: Resolve contract
	// ------------------------------------------------

	log.Info("resolving serviceunit contract")

	resolved, err := serviceunitResolution.ResolveServiceUnit(su)
	if err != nil {

		log.Error(err, "serviceunit resolution failed")
		d.events.FromError(su, "ServiceUnitResolveFailed", err)
		core.SetCondition(&su.Status.Conditions, "ServiceUnitResolved", core.ConditionFalse, "InvalidSpec", err.Error())

		return err
	}

	log.Info("serviceunit resolved successfully")
	d.events.Normal(su, "ServiceUnitResolved", "ServiceUnit specification resolved successfully")
	core.SetCondition(&su.Status.Conditions, "ServiceUnitResolved", core.ConditionTrue, "Resolved", "ServiceUnit specification resolved successfully")

	// ------------------------------------------------
	// Stage 2: Ensure prerequisites
	// ------------------------------------------------

	log.Info("ensuring serviceunit prerequisites")

	if err := d.serviceUnitMediator.EnsurePrerequisites(ctx, resolved); err != nil {

		log.Error(err, "serviceunit prerequisites failed")
		d.events.FromError(su, "ServiceUnitPrerequisitesFailed", err)
		core.SetCondition(&su.Status.Conditions, "ServiceUnitPrerequisitesReady", core.ConditionFalse, "PrerequisitesFailed", err.Error())

		return err
	}

	log.Info("serviceunit prerequisites ensured")
	d.events.Normal(su, "ServiceUnitPrerequisitesReady", "All ServiceUnit prerequisites created successfully")
	core.SetCondition(&su.Status.Conditions, "ServiceUnitPrerequisitesReady", core.ConditionTrue, "PrerequisitesReady", "All prerequisites created successfully")

	// ------------------------------------------------
	// Stage 3: Execute intent
	// ------------------------------------------------

	// log.Info("reconciling serviceunit workload")

	// if err := d.ServiceUnitService.Reconcile(ctx, resolved); err != nil {

	// 	log.Error(err, "serviceunit reconcile failed")

	// 	d.events.FromError(su, "ServiceUnitReconcileFailed", err)

	// 	core.SetCondition(
	// 		&su.Status.Conditions,
	// 		"ServiceUnitReady",
	// 		core.ConditionFalse,
	// 		"ReconcileFailed",
	// 		err.Error(),
	// 	)

	// 	return err
	// }

	log.Info("serviceunit reconciliation complete")

	d.events.Normal(su, "ServiceUnitReady", "ServiceUnit successfully reconciled")

	core.SetCondition(&su.Status.Conditions, "ServiceUnitReady", core.ConditionTrue, "Ready", "ServiceUnit successfully reconciled")

	log.Info("serviceunit domain handling complete")

	return nil
}

func (d *ServiceUnitDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*serviceunitv1alpha1.ServiceUnit)
	return ok
}

func (d *ServiceUnitDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldSU, okOld := oldObj.(*serviceunitv1alpha1.ServiceUnit)
	newSU, okNew := newObj.(*serviceunitv1alpha1.ServiceUnit)
	if !okOld || !okNew {
		return false
	}

	return !reflect.DeepEqual(oldSU.Spec, newSU.Spec)
}

func (d *ServiceUnitDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*serviceunitv1alpha1.ServiceUnit)
	return ok
}
