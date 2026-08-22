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
Package route implements the Route resource domain.

The Route domain is responsible for managing the lifecycle of Route
resources. It receives commands from the Engine, resolves resource
specifications into validated contracts, delegates processing to the
application layer, and records reconciliation outcomes through conditions
and events.

Unlike Build, Deployment, and Package, Route has no cross-cutting
prerequisites of its own (no secrets, no ServiceAccounts, no RBAC to
provision) — RouteService dispatches directly to the selected backend
provider (Knative or Ingress) with no mediator stage in between.
*/
package route

import (
	"context"
	"fmt"
	"reflect"

	networksv1alpha1 "github.com/blanketops/environments-api/api/networks/v1alpha1"
	libroute "github.com/blanketops/environments/cache/route"
	"github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/conditions"
	"github.com/blanketops/environments/core/events"
	"github.com/blanketops/environments/pkg/apis/route/application"
	routeResolution "github.com/blanketops/environments/resolution/route/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RouteDomain implements the Route resource domain logic.
type RouteDomain struct {
	// routeService handles business logic for route operations: mapping,
	// backend selection, provider dispatch, and status persistence.
	routeService *application.RouteService

	// routeCache provides generation-scoped, field-level caching for
	// Route resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	routeCache *libroute.RouteCache

	// events handles logging of Kubernetes events.
	events *events.EventRecorder

	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new RouteDomain instance configured with the necessary dependencies.
func New(routeService *application.RouteService, domainCache *cache.Cache, eventRecorder *events.EventRecorder, log logr.Logger) *RouteDomain {
	return &RouteDomain{
		routeService: routeService,
		routeCache:   libroute.NewRouteCache(domainCache),
		events:       eventRecorder,
		log:          log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *RouteDomain) GVK() schema.GroupVersionKind {
	return networksv1alpha1.GroupVersion.WithKind("Route")
}

// Handle executes command.Command operations routed by the Engine.
func (d *RouteDomain) Handle(ctx context.Context, cmd command.Command) error {

	routeCR, ok := cmd.Obj.(*networksv1alpha1.Route)
	if !ok || routeCR == nil {
		return fmt.Errorf("invalid object passed to RouteDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "route", "name", routeCR.Name, "namespace", routeCR.Namespace)
	log.Info("handling route command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(routeCR)
	gen := routeCR.GetGeneration()

	switch cmd.Type {
	case command.CmdCreate, command.CmdUpdate:

		// Stage 0: Resolve Route contract
		log.Info("resolving route contract")
		resolved, err := routeResolution.ResolveRoute(routeCR)
		if err != nil {
			log.Error(err, "route resolution failed")
			d.events.FromError(routeCR, "RouteResolveFailed", err)
			conditions.SetCondition(&routeCR.Status.Conditions, "RouteResolveFailed", conditions.ConditionFalse, "RouteResolve", err.Error())
			return err
		}

		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		if cerr := d.routeCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
			d.events.FromError(routeCR, "RouteCacheFailed", cerr)
			conditions.SetCondition(&routeCR.Status.Conditions, "RouteCacheFailed", conditions.ConditionFalse, "resolved projection publish incomplete", cerr.Error())
		}

		log.Info("route resolved successfully")
		d.events.Normal(routeCR, "RouteResolve", "Route specification resolved successfully")
		conditions.SetCondition(&routeCR.Status.Conditions, "RouteResolved", conditions.ConditionTrue, "RouteSpecResolved", "Route specification resolved successfully")

		log.Info("route cached successfully")
		d.events.Normal(routeCR, "RouteCache", "Route specification cached successfully")
		conditions.SetCondition(&routeCR.Status.Conditions, "RouteCached", conditions.ConditionTrue, "RouteSpecCached", "Route specification cached successfully")

		// Stage 2: Map, select backend, dispatch, and persist status.
		// RouteService owns condition derivation and status persistence for
		// the reconciliation outcome itself (RouteReady/RoutePending/
		// RouteFailed/RouteDegraded) — separate from the domain-level
		// conditions this Handle sets above.
		log.Info("dispatching route to backend provider")
		if err := d.routeService.Reconcile(ctx, resolved); err != nil {
			log.Error(err, "route reconciliation failed")
			d.events.FromError(routeCR, "RouteFailed", err)
			return err
		}

		log.Info("route domain handling complete")
		d.events.Normal(routeCR, "RouteSucceeded", "route reconciliation completed successfully")

	case command.CmdDelete:
		// Real teardown, gated by finalizer at the controller level.
		// Handle() must return nil ONLY if it is safe for the
		// controller to remove the finalizer and let K8s finish
		// deleting the object. Any error here keeps the finalizer
		// in place and the controller will retry on next reconcile.
		log.Info("route teardown requested")

		resolved, err := routeResolution.ResolveRoute(routeCR)
		if err != nil {
			log.Error(err, "resolution failed during teardown")
			d.events.FromError(routeCR, "RouteTeardownResolveFailed", err)
			conditions.SetCondition(&routeCR.Status.Conditions, "RouteDeleted", conditions.ConditionFalse, "RouteTeardownResolveFailed", err.Error())
			return err
		}

		if err := d.routeService.Teardown(ctx, resolved); err != nil {
			log.Error(err, "route teardown failed")
			d.events.FromError(routeCR, "RouteTeardownFailed", err)
			conditions.SetCondition(&routeCR.Status.Conditions, "RouteDeleted", conditions.ConditionFalse, "RouteTeardownFailed", err.Error())
			return err
		}

		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.routeCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}

		log.Info("route teardown complete")
		d.events.Normal(routeCR, "RouteDeleted", "route and owned resources cleaned up successfully")
		conditions.SetCondition(&routeCR.Status.Conditions, "RouteDeleted", conditions.ConditionTrue, "RouteCleanupComplete", "route and owned resources cleaned up successfully")
	}
	return nil
}

// CanCreate reports whether the supplied object can be processed as a Route create operation.
func (d *RouteDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*networksv1alpha1.Route)
	return ok
}

// CanUpdate reports whether the supplied update should trigger Route reconciliation
// by comparing the specifications of the old and new objects.
func (d *RouteDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldR, okOld := oldObj.(*networksv1alpha1.Route)
	newR, okNew := newObj.(*networksv1alpha1.Route)

	// If either object is not a Route, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldR.Spec, newR.Spec)
}

// CanDelete reports whether the supplied object can be processed as a Route delete operation.
func (d *RouteDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*networksv1alpha1.Route)
	return ok
}
