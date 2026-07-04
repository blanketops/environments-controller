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
Package githubevent implements the GitHubEvent resource domain.

The GitHubEvent domain is responsible for managing the lifecycle of
GitHubEvent resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, delegates
processing to the application layer, and records reconciliation
outcomes through conditions and events.
*/
package githubevent

import (
	"context"
	"fmt"
	"reflect"

	eventsv1alpha1 "github.com/BlanketOps/environments-api/api/events/v1alpha1"
	"github.com/go-logr/logr"
	libgithubevent "github.com/ntlaletsi70/blanketops-environments/cache/githubevent"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/githubevent/application"
	githubEventResolution "github.com/ntlaletsi70/blanketops-environments/resolution/githubevent"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	githubEventMediator "github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/githubevent"
)

// GitHubEventDomain implements the GitHubEvent resource domain.
type GitHubEventDomain struct {
	// githubEventMediator manages prerequisite interactions.
	githubEventMediator *githubEventMediator.Mediator
	// githubEventService handles business logic for githubevent operations.
	githubEventService *application.GitHubEventService
	// githubeventCache provides generation-scoped, field-level caching for
	// GitHubEvent resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	githubEventCache *libgithubevent.GitHubEventCache
	// events handles logging of Kubernetes events.
	events *core.EventRecorder
	// log is the logger instance for this domain.
	log logr.Logger
}

// New constructs a new GitHubEventDomain instance.
func New(buildService *application.GitHubEventService, githubEventMediator *githubEventMediator.Mediator, events *core.EventRecorder, cache *core.Cache, log logr.Logger) *GitHubEventDomain {
	return &GitHubEventDomain{
		githubEventMediator: githubEventMediator,
		githubEventService:  buildService,
		githubEventCache:    libgithubevent.NewGitHubEventCache(cache),
		events:              events,
		log:                 log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *GitHubEventDomain) GVK() schema.GroupVersionKind {
	return eventsv1alpha1.GroupVersion.WithKind("GitHubEvent")
}

// Handle executes core.Command operations routed by the Engine.
func (d *GitHubEventDomain) Handle(ctx context.Context, cmd core.Command) error {

	githubeventCR, ok := cmd.Obj.(*eventsv1alpha1.GitHubEvent)
	if !ok || githubeventCR == nil {
		return fmt.Errorf("invalid object passed to GitHubEventDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "githubevent", "name", githubeventCR.Name, "namespace", githubeventCR.Namespace)
	log.Info("handling githubevent command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(githubeventCR)
	gen := githubeventCR.GetGeneration()

	switch cmd.Type {
	case core.CmdCreate, core.CmdUpdate:

		// ---------------------------------------------------------
		// 0. Resolve GitHubEvent contract ONCE
		// ---------------------------------------------------------
		log.Info("resolving githubevent contract")
		resolved, err := githubEventResolution.ResolveGitHubEvent(githubeventCR)
		if err != nil {
			log.Error(err, "githubevent resolution failed")
			d.events.FromError(githubeventCR, "GitHubEventResolveFailed", err)
			core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventResolveFailed", core.ConditionFalse, "GitHubEventResolve", err.Error())
			return err
		}

		// ------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		// ------------------------------------------------
		if cerr := d.githubEventCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
			d.events.FromError(githubeventCR, "GitHubEventCacheFailed", cerr)
			core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventCacheFailed", core.ConditionFalse, "resolved projection publish incomplete", cerr.Error())
		}

		log.Info("githubevent resolved successfully")
		d.events.Normal(githubeventCR, "GitHubEventResolve", "GitHubEvent specification resolved successfully")
		core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventResolved", core.ConditionTrue, "GitHubEventResolved", "GitHubEvent specification resolved successfully")

		log.Info("githubevent cached successfully")
		d.events.Normal(githubeventCR, "GitHubEventCache", "GitHubEvent specification cached successfully")
		core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventCached", core.ConditionTrue, "GitHubEventSpecCached", "GitHubEvent specification cached successfully")

		// -----------------------------------------------------------
		// 2. Ensure prerequisites (secrets, webhooks, etc.)
		// -----------------------------------------------------------
		log.Info("create githubevent prerequisites")
		if err := d.githubEventMediator.EnsurePrerequisites(ctx, resolved); err != nil {
			log.Error(err, "githubevent prerequisites failed")
			d.events.FromError(githubeventCR, "GitHubEventPrerequisitesCreateFailed", err)
			core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventPrerequisitesCreateFailed", core.ConditionFalse, "GitHubEventPrerequisitesFailed", err.Error())
			return err
		}

		log.Info("githubevent prerequisites created")
		d.events.Normal(githubeventCR, "GitHubEventPrerequisitesCreate", "All githubevent prerequisites created successfully")
		core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventPrerequisitesCreated", core.ConditionTrue, "GitHubEventPrerequisitesReady", "All prerequisites created successfully")

		// ---------------------------------------------------------
		// 3. Domain application logic
		// ---------------------------------------------------------
		log.Info("triggering githubevent execution")
		if err := d.githubEventService.Reconcile(ctx, resolved); err != nil {
			log.Error(err, "triggering githubevent failed")
			d.events.FromError(githubeventCR, "GitHubEventRejected", err)
			core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventOrganized", core.ConditionFalse, "GitHubEventRejected", err.Error())
			return err
		}

		// --------------------------------------------------------
		// 4. Execution requested
		// --------------------------------------------------------
		log.Info("githubevent execution requested")
		d.events.Normal(githubeventCR, "GitHubEventOrganized", "GitHubEvent organization process  has started")
		core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventOrganized", core.ConditionTrue, "GitHubEventOrganized", "GitHubEvent organization has started")
		log.Info("githubevent domain handling complete")

	case core.CmdDelete:
		// --------------------------------------------------------
		// Real teardown, gated by finalizer at the controller level.
		// Handle() must return nil ONLY if it is safe for the
		// controller to remove the finalizer and let K8s finish
		// deleting the object. Any error here keeps the finalizer
		// in place and the controller will retry on next reconcile.
		// --------------------------------------------------------
		log.Info("githubevent teardown requested")
		resolved, err := githubEventResolution.ResolveGitHubEvent(githubeventCR)
		if err != nil {
			log.Error(err, "resolution failed during teardown")
			d.events.FromError(githubeventCR, "GitHubEventTeardownResolveFailed", err)
			core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventDeleted", core.ConditionFalse, "GitHubEventTeardownResolveFailed", err.Error())
			return err
		}

		// Tear down prerequisites the mediator created (secrets, SAs, RBAC).
		if err := d.githubEventMediator.CleanupPrerequisites(ctx, resolved); err != nil {
			log.Error(err, "prerequisites cleanup failed")
			d.events.FromError(githubeventCR, "GitHubEventPrerequisitesCleanupFailed", err)
			core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventDeleted", core.ConditionFalse, "GitHubEventPrerequisitesCleanupFailed", err.Error())
			return err
		}

		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.githubEventCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}

		log.Info("githubevent teardown complete")
		d.events.Normal(githubeventCR, "GitHubEventDeleted", "githubevent and owned resources cleaned up successfully")
		core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventDeleted", core.ConditionTrue, "GitHubEventCleanupComplete", "githubevent and owned resources cleaned up successfully")
	}
	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

// CanCreate reports whether the supplied object can be processed as a GitHubEvent create operation.
func (d *GitHubEventDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*eventsv1alpha1.GitHubEvent)
	return ok
}

// CanUpdate reports whether the supplied update should trigger GitHubEvent reconciliation
// by comparing the specifications of the old and new objects.
func (d *GitHubEventDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldEv, okOld := oldObj.(*eventsv1alpha1.GitHubEvent)
	newEv, okNew := newObj.(*eventsv1alpha1.GitHubEvent)

	// If either object is not a GitHubEvent, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	// Reconcile only on spec changes
	return !reflect.DeepEqual(oldEv.Spec, newEv.Spec)
}

// CanDelete reports whether the supplied object can be processed as a GitHubEvent delete operation.
func (d *GitHubEventDomain) CanDelete(obj client.Object) bool {
	// Events are historical facts; nothing to undo
	return false
}
