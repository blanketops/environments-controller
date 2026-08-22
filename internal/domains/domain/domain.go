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
Package domain implements the Domain resource domain.

The Domain domain is responsible for managing the lifecycle of Domain
resources. It receives commands from the Engine, resolves resource
specifications into validated contracts, delegates processing to the
application layer, and records reconciliation outcomes through conditions
and events.

Like Route, Domain has no cross-cutting prerequisites of its own (no
secrets, no ServiceAccounts, no RBAC to provision) — DomainService
dispatches directly to the Knative provider with no mediator stage in
between.

Domain publishes only its resolved spec to the cache (PublishResolved).
DomainCache also exposes PublishStatus for scalar status fields
(domainReady, certificateRef, domainMappingRef) that the Route mediator
will eventually read to gate DomainMapping dispatch — but DomainService's
Reconcile does not yet return the values PublishStatus needs, so that call
is left for whichever follow-up builds the Route mediator's Domain
integration rather than wired here with fabricated inputs.
*/
package domain

import (
	"context"
	"fmt"
	"reflect"

	networksv1alpha1 "github.com/blanketops/environments-api/api/networks/v1alpha1"
	libdomain "github.com/blanketops/environments/cache/domain"
	"github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/conditions"
	"github.com/blanketops/environments/core/events"
	"github.com/blanketops/environments/pkg/apis/domain/application"
	domainResolution "github.com/blanketops/environments/resolution/domain/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DomainDomain implements the Domain resource domain logic.
type DomainDomain struct {
	// domainService handles business logic for domain operations: mapping,
	// backend selection, provider dispatch, and status persistence.
	domainService *application.DomainService

	// domainCache provides generation-scoped, field-level caching for
	// Domain resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	domainCache *libdomain.DomainCache

	// events handles logging of Kubernetes events.
	events *events.EventRecorder

	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new DomainDomain instance configured with the necessary dependencies.
func New(domainService *application.DomainService, domainCache *cache.Cache, eventRecorder *events.EventRecorder, log logr.Logger) *DomainDomain {
	return &DomainDomain{
		domainService: domainService,
		domainCache:   libdomain.NewDomainCache(domainCache),
		events:        eventRecorder,
		log:           log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *DomainDomain) GVK() schema.GroupVersionKind {
	return networksv1alpha1.GroupVersion.WithKind("Domain")
}

// Handle executes command.Command operations routed by the Engine.
func (d *DomainDomain) Handle(ctx context.Context, cmd command.Command) error {

	domainCR, ok := cmd.Obj.(*networksv1alpha1.Domain)
	if !ok || domainCR == nil {
		return fmt.Errorf("invalid object passed to DomainDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "domain", "name", domainCR.Name, "namespace", domainCR.Namespace)
	log.Info("handling domain command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(domainCR)
	gen := domainCR.GetGeneration()

	switch cmd.Type {
	case command.CmdCreate, command.CmdUpdate:

		// Stage 0: Resolve Domain contract
		log.Info("resolving domain contract")
		resolved, err := domainResolution.ResolveDomain(domainCR)
		if err != nil {
			log.Error(err, "domain resolution failed")
			d.events.FromError(domainCR, "DomainResolveFailed", err)
			conditions.SetCondition(&domainCR.Status.Conditions, "DomainResolveFailed", conditions.ConditionFalse, "DomainResolve", err.Error())
			return err
		}

		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		if cerr := d.domainCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
			d.events.FromError(domainCR, "DomainCacheFailed", cerr)
			conditions.SetCondition(&domainCR.Status.Conditions, "DomainCacheFailed", conditions.ConditionFalse, "resolved projection publish incomplete", cerr.Error())
		}

		log.Info("domain resolved successfully")
		d.events.Normal(domainCR, "DomainResolve", "Domain specification resolved successfully")
		conditions.SetCondition(&domainCR.Status.Conditions, "DomainResolved", conditions.ConditionTrue, "DomainSpecResolved", "Domain specification resolved successfully")

		log.Info("domain cached successfully")
		d.events.Normal(domainCR, "DomainCache", "Domain specification cached successfully")
		conditions.SetCondition(&domainCR.Status.Conditions, "DomainCached", conditions.ConditionTrue, "DomainSpecCached", "Domain specification cached successfully")

		// Stage 2: Map, select backend, dispatch, and persist status.
		// DomainService owns condition derivation and status persistence for
		// the reconciliation outcome itself (Ready/DomainClaimReady/
		// CertificateReady) — separate from the domain-level conditions this
		// Handle sets above. ErrCertProvisioning is returned unwrapped so the
		// controller requeues while ACME issuance is in progress.
		log.Info("dispatching domain to provider")
		if err := d.domainService.Reconcile(ctx, resolved); err != nil {
			log.Error(err, "domain reconciliation failed")
			d.events.FromError(domainCR, "DomainFailed", err)
			return err
		}

		log.Info("domain domain handling complete")
		d.events.Normal(domainCR, "DomainSucceeded", "domain reconciliation completed successfully")

	case command.CmdDelete:
		// Real teardown, gated by finalizer at the controller level.
		// Handle() must return nil ONLY if it is safe for the
		// controller to remove the finalizer and let K8s finish
		// deleting the object. Any error here keeps the finalizer
		// in place and the controller will retry on next reconcile.
		log.Info("domain teardown requested")

		resolved, err := domainResolution.ResolveDomain(domainCR)
		if err != nil {
			log.Error(err, "resolution failed during teardown")
			d.events.FromError(domainCR, "DomainTeardownResolveFailed", err)
			conditions.SetCondition(&domainCR.Status.Conditions, "DomainDeleted", conditions.ConditionFalse, "DomainTeardownResolveFailed", err.Error())
			return err
		}

		if err := d.domainService.Teardown(ctx, resolved); err != nil {
			log.Error(err, "domain teardown failed")
			d.events.FromError(domainCR, "DomainTeardownFailed", err)
			conditions.SetCondition(&domainCR.Status.Conditions, "DomainDeleted", conditions.ConditionFalse, "DomainTeardownFailed", err.Error())
			return err
		}

		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.domainCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}

		log.Info("domain teardown complete")
		d.events.Normal(domainCR, "DomainDeleted", "domain and owned resources cleaned up successfully")
		conditions.SetCondition(&domainCR.Status.Conditions, "DomainDeleted", conditions.ConditionTrue, "DomainCleanupComplete", "domain and owned resources cleaned up successfully")
	}
	return nil
}

// CanCreate reports whether the supplied object can be processed as a Domain create operation.
func (d *DomainDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*networksv1alpha1.Domain)
	return ok
}

// CanUpdate reports whether the supplied update should trigger Domain reconciliation
// by comparing the specifications of the old and new objects.
func (d *DomainDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldD, okOld := oldObj.(*networksv1alpha1.Domain)
	newD, okNew := newObj.(*networksv1alpha1.Domain)

	// If either object is not a Domain, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldD.Spec, newD.Spec)
}

// CanDelete reports whether the supplied object can be processed as a Domain delete operation.
func (d *DomainDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*networksv1alpha1.Domain)
	return ok
}
