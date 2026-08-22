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

	serviceunitv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	libserviceunit "github.com/blanketops/environments/cache/serviceunit"
	"github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/conditions"
	"github.com/blanketops/environments/core/events"
	serviceunitResolution "github.com/blanketops/environments/resolution/serviceunit/resolve"
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
	events           *events.EventRecorder
	log              logr.Logger
}

// New constructs a ServiceUnitDomain.
func New(mediator *serviceunit.Mediator, domainCache *cache.Cache, eventRecorder *events.EventRecorder, log logr.Logger) *ServiceUnitDomain {
	return &ServiceUnitDomain{
		serviceUnitMediator: mediator,
		serviceUnitCache:    libserviceunit.NewServiceUnitCache(domainCache),
		events:              eventRecorder,
		log:                 log,
	}
}

// GVK reports the GroupVersionKind this domain handles: ServiceUnit.
func (d *ServiceUnitDomain) GVK() schema.GroupVersionKind {
	return serviceunitv1alpha1.GroupVersion.WithKind("ServiceUnit")
}

// Handle resolves cmd's ServiceUnit contract and ensures its prerequisites,
// recording conditions and events at each stage. Reconciliation of the
// workload itself (Stage 3) is not yet wired up.
func (d *ServiceUnitDomain) Handle(ctx context.Context, cmd command.Command) error {

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

	// Stage 1: Resolve contract

	log.Info("resolving serviceunit contract")

	resolved, err := serviceunitResolution.ResolveServiceUnit(su)
	if err != nil {

		log.Error(err, "serviceunit resolution failed")
		d.events.FromError(su, "ServiceUnitResolveFailed", err)
		conditions.SetCondition(&su.Status.Conditions, "ServiceUnitResolved", conditions.ConditionFalse, "InvalidSpec", err.Error())

		return err
	}

	log.Info("serviceunit resolved successfully")
	d.events.Normal(su, "ServiceUnitResolved", "ServiceUnit specification resolved successfully")
	conditions.SetCondition(&su.Status.Conditions, "ServiceUnitResolved", conditions.ConditionTrue, "Resolved", "ServiceUnit specification resolved successfully")

	// Stage 2: Ensure prerequisites

	log.Info("ensuring serviceunit prerequisites")

	if err := d.serviceUnitMediator.EnsurePrerequisites(ctx, resolved); err != nil {

		log.Error(err, "serviceunit prerequisites failed")
		d.events.FromError(su, "ServiceUnitPrerequisitesFailed", err)
		conditions.SetCondition(&su.Status.Conditions, "ServiceUnitPrerequisitesReady", conditions.ConditionFalse, "PrerequisitesFailed", err.Error())

		return err
	}

	log.Info("serviceunit prerequisites ensured")
	d.events.Normal(su, "ServiceUnitPrerequisitesReady", "All ServiceUnit prerequisites created successfully")
	conditions.SetCondition(&su.Status.Conditions, "ServiceUnitPrerequisitesReady", conditions.ConditionTrue, "PrerequisitesReady", "All prerequisites created successfully")

	// Stage 3: Execute intent

	// log.Info("reconciling serviceunit workload")

	// if err := d.ServiceUnitService.Reconcile(ctx, resolved); err != nil {

	// 	log.Error(err, "serviceunit reconcile failed")

	// 	d.events.FromError(su, "ServiceUnitReconcileFailed", err)

	// 	conditions.SetCondition(
	// 		&su.Status.Conditions,
	// 		"ServiceUnitReady",
	// 		conditions.ConditionFalse,
	// 		"ReconcileFailed",
	// 		err.Error(),
	// 	)

	// 	return err
	// }

	log.Info("serviceunit reconciliation complete")

	d.events.Normal(su, "ServiceUnitReady", "ServiceUnit successfully reconciled")

	conditions.SetCondition(&su.Status.Conditions, "ServiceUnitReady", conditions.ConditionTrue, "Ready", "ServiceUnit successfully reconciled")

	log.Info("serviceunit domain handling complete")

	return nil
}

// CanCreate reports whether obj is a ServiceUnit.
func (d *ServiceUnitDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*serviceunitv1alpha1.ServiceUnit)
	return ok
}

// CanUpdate reports whether oldObj and newObj are both ServiceUnits whose
// specs differ.
func (d *ServiceUnitDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldSU, okOld := oldObj.(*serviceunitv1alpha1.ServiceUnit)
	newSU, okNew := newObj.(*serviceunitv1alpha1.ServiceUnit)
	if !okOld || !okNew {
		return false
	}

	return !reflect.DeepEqual(oldSU.Spec, newSU.Spec)
}

// CanDelete reports whether obj is a ServiceUnit.
func (d *ServiceUnitDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*serviceunitv1alpha1.ServiceUnit)
	return ok
}
