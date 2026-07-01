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

The Route domain manages the lifecycle of Route resources. It receives commands
from the Engine, resolves the raw contract into a validated ResolvedRoute,
publishes the projection to the advisory cache, gates on prerequisite coherence
(the TLS Secret), and delegates materialization to the application service.

Route's single prerequisite is the TLS Secret. When TLSEnabled is true the
DomainMapping references CertSecretName(host); the mediator holds the route
until that Secret exists rather than materializing a DomainMapping against an
incoherent domain. Route provisions no secrets itself — that is the Domain
domain's responsibility.
*/
package route

// import (
// 	"context"
// 	"errors"
// 	"fmt"
// 	"reflect"
//

// 	"github.com/go-logr/logr"
// 	networksv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/apis/api/networks/v1alpha1"
// 	libroute "github.com/ntlaletsi70/blanketops-environments/cache/route"
// 	"github.com/ntlaletsi70/blanketops-environments/core"
// 	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/routes/application"
// 	routedomain "github.com/ntlaletsi70/blanketops-environments/pkg/apis/routes/domain"
// 	routeResolution "github.com/ntlaletsi70/blanketops-environments/resolution/route"
// 	"k8s.io/apimachinery/pkg/runtime/schema"
// 	"sigs.k8s.io/controller-runtime/pkg/client"

// 	routemediator "github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/route"
// )

// // RouteDomain implements the Route resource domain logic.
// type RouteDomain struct {
// 	// routeMediator gates materialization on prerequisite coherence (TLS Secret).
// 	routeMediator *routemediator.Mediator

// 	// routeService handles business logic for route operations:
// 	// map → select provider → Ensure → write status.
// 	routeService *application.RouteService

// 	// routeCache provides generation-scoped, field-level caching for Route
// 	// resources. Advisory only: misses and errors fall through to full
// 	// computation; correctness never depends on a hit.
// 	routeCache *libroute.RouteCache

// 	// events handles logging of Kubernetes events.
// 	events *core.EventRecorder

// 	// log is the logger instance for this domain.
// 	log logr.Logger
// }

// // New returns a new RouteDomain configured with the necessary dependencies.
// func New(routeMediator *routemediator.Mediator, routeService *application.RouteService, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *RouteDomain {
// 	return &RouteDomain{
// 		routeMediator: routeMediator,
// 		routeService:  routeService,
// 		routeCache:    libroute.NewRouteCache(cache),
// 		events:        events,
// 		log:           log,
// 	}
// }

// // GVK tells the engine which CRD this domain handles.
// func (d *RouteDomain) GVK() schema.GroupVersionKind {
// 	return networksv1alpha1.GroupVersion.WithKind("Route")
// }

// // Handle executes core.Command operations routed by the Engine.
// func (d *RouteDomain) Handle(ctx context.Context, cmd core.Command) error {
// 	routeCR, ok := cmd.Obj.(*networksv1alpha1.Route)
// 	if !ok || routeCR == nil {
// 		return fmt.Errorf("invalid object passed to RouteDomain: %T", cmd.Obj)
// 	}

// 	log := d.log.WithValues("domain", "route", "name", routeCR.Name, "namespace", routeCR.Namespace)
// 	log.Info("handling route command", "type", cmd.Type)

// 	nn := client.ObjectKeyFromObject(routeCR)
// 	gen := routeCR.GetGeneration()

// 	switch cmd.Type {
// 	case core.CmdCreate, core.CmdUpdate:
// 		//------------------------------------------------
// 		// Stage 0: Resolve route contract
// 		//------------------------------------------------
// 		log.Info("resolving route contract")
// 		resolved, err := routeResolution.ResolveRoute(routeCR)
// 		if err != nil {
// 			log.Error(err, "route resolution failed")
// 			d.events.FromError(routeCR, "RouteResolveFailed", err)
// 			core.SetCondition(&routeCR.Status.Conditions, "RouteResolveFailed", core.ConditionFalse, "RouteResolve", err.Error())
// 			return err
// 		}

// 		//------------------------------------------------
// 		// Stage 1: Publish resolved projection to cache
// 		// (advisory — failures never block reconciliation)
// 		//------------------------------------------------
// 		if cerr := d.routeCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
// 			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
// 			d.events.FromError(routeCR, "RouteCacheFailed", cerr)
// 			core.SetCondition(&routeCR.Status.Conditions, "RouteCacheFailed", core.ConditionFalse, "resolved projection publish incomplete", cerr.Error())
// 		}

// 		log.Info("route resolved successfully")
// 		d.events.Normal(routeCR, "RouteResolve", "Route specification resolved successfully")
// 		core.SetCondition(&routeCR.Status.Conditions, "RouteResolved", core.ConditionTrue, "RouteSpecResolved", "Route specification resolved successfully")

// 		log.Info("route cached successfully")
// 		d.events.Normal(routeCR, "RouteCache", "Route specification cached successfully")
// 		core.SetCondition(&routeCR.Status.Conditions, "RouteCached", core.ConditionTrue, "RouteSpecCached", "Route specification cached successfully")

// 		//------------------------------------------------
// 		// Stage 2: Ensure prerequisites — gate on the TLS Secret.
// 		// A pending Secret is a non-terminal hold: the predicate is not yet
// 		// satisfied, so we requeue without materializing a DomainMapping
// 		// against an incoherent domain.
// 		//------------------------------------------------
// 		log.Info("ensuring route prerequisites")
// 		if err := d.routeMediator.EnsurePrerequisites(ctx, resolved); err != nil {
// 			if errors.Is(err, routedomain.ErrTLSSecretPending) {
// 				log.Info("route held: TLS secret not yet provisioned")
// 				d.events.Normal(routeCR, "RouteTLSPending", "Waiting for TLS secret to be provisioned")
// 				core.SetCondition(&routeCR.Status.Conditions, "TLSReady", core.ConditionFalse, "SecretPending", "Waiting for TLS secret to be provisioned")
// 				return err
// 			}
// 			log.Error(err, "route prerequisites failed")
// 			d.events.FromError(routeCR, "RoutePrerequisitesFailed", err)
// 			core.SetCondition(&routeCR.Status.Conditions, "RoutePrerequisitesFailed", core.ConditionFalse, "route prerequisites failed, internal error", err.Error())
// 			return err
// 		}

// 		log.Info("route prerequisites satisfied")
// 		d.events.Normal(routeCR, "RoutePrerequisites", "All route prerequisites satisfied")
// 		core.SetCondition(&routeCR.Status.Conditions, "TLSReady", core.ConditionTrue, "TLSReady", "TLS secret present or not required")

// 		//------------------------------------------------
// 		// Stage 3: Reconcile — map, select provider, Ensure
// 		// DomainMapping, write execution conditions.
// 		//------------------------------------------------
// 		log.Info("reconciling route")
// 		if err := d.routeService.Reconcile(ctx, resolved); err != nil {
// 			log.Error(err, "route reconcile failed")
// 			d.events.FromError(routeCR, "RouteReconcileFailed", err)
// 			core.SetCondition(&routeCR.Status.Conditions, "RouteReconcileFailed", core.ConditionFalse, "reconciling route, internal error", err.Error())
// 			return err
// 		}

// 		log.Info("route reconciled")
// 		d.events.Normal(routeCR, "RouteReconciled", "Route materialized successfully")
// 		core.SetCondition(&routeCR.Status.Conditions, "RouteReconciled", core.ConditionTrue, "RouteMaterialized", "Route materialized successfully")
// 		log.Info("route domain handling complete")

// 	case core.CmdDelete:
// 		// Drop the projection for this object (all generations). The
// 		// DomainMapping is garbage-collected via ownerReference — no explicit
// 		// teardown required here.
// 		if cerr := d.routeCache.Invalidate(ctx, nn); cerr != nil {
// 			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
// 		}
// 		log.Info("route deletion marked")
// 		d.events.Normal(routeCR, "RouteDeletion", "route deleted; DomainMapping cascade-deleted via ownerReference")
// 		core.SetCondition(&routeCR.Status.Conditions, "RouteDeleted", core.ConditionTrue, "RouteMarkedDelete", "route deleted; DomainMapping cascade-deleted via ownerReference")
// 		log.Info("route domain handling complete")
// 	}

// 	return nil
// }

// // -----------------------------------------------------------------------------
// // Predicate hooks
// // -----------------------------------------------------------------------------

// // CanCreate reports whether the supplied object can be processed as a Route create.
// func (d *RouteDomain) CanCreate(obj client.Object) bool {
// 	_, ok := obj.(*networksv1alpha1.Route)
// 	return ok
// }

// // CanUpdate reports whether the supplied update should trigger Route
// // reconciliation by comparing the specifications of the old and new objects.
// func (d *RouteDomain) CanUpdate(oldObj, newObj client.Object) bool {
// 	oldR, okOld := oldObj.(*networksv1alpha1.Route)
// 	newR, okNew := newObj.(*networksv1alpha1.Route)
// 	if !okOld || !okNew {
// 		return false
// 	}
// 	// Reconcile only on spec changes.
// 	return !reflect.DeepEqual(oldR.Spec, newR.Spec)
// }

// // CanDelete reports whether the supplied object can be processed as a Route delete.
// func (d *RouteDomain) CanDelete(obj client.Object) bool {
// 	_, ok := obj.(*networksv1alpha1.Route)
// 	return ok
// }
