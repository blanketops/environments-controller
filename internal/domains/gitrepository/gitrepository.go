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
Package gitrepository implements the GitRepository resource domain.

The GitRepository domain is responsible for managing the lifecycle of
GitRepository resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, delegates
processing to the application layer, and records reconciliation
outcomes through conditions and events.
*/

package gitrepository

import (
	"context"
	"fmt"
	"reflect"

	sourcesv1alpha1 "github.com/blanketops/environments-api/api/sources/v1alpha1"
	libgitrepository "github.com/blanketops/environments/cache/gitrepository"
	"github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/conditions"
	"github.com/blanketops/environments/core/events"
	"github.com/blanketops/environments/pkg/apis/gitrepository/application"
	gitrepoResolution "github.com/blanketops/environments/resolution/gitrepository/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/mediators/gitrepository"
)

// GitRepositoryDomain implements the GitRepository resource domain logic.
type GitRepositoryDomain struct {
	// gitRepositoryMediator manages prerequisite interactions.
	gitRepositoryMediator *gitrepository.Mediator

	// gitRepositoryService handles business logic for gitrepository operations.
	gitRepositoryService *application.GitRepositoryService

	// gitRepositoryCache provides generation-scoped, field-level caching for
	// GitRepository resources. Advisory only: misses and errors fall through
	// to full computation; correctness never depends on a hit.
	gitRepositoryCache *libgitrepository.GitRepositoryCache

	// events handles logging of Kubernetes events.
	events *events.EventRecorder

	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new GitRepositoryDomain instance configured with the necessary dependencies.
func New(gitRepositoryMediator *gitrepository.Mediator, gitRepositoryService *application.GitRepositoryService, domainCache *cache.Cache, eventRecorder *events.EventRecorder, log logr.Logger) *GitRepositoryDomain {
	return &GitRepositoryDomain{
		gitRepositoryMediator: gitRepositoryMediator,
		gitRepositoryService:  gitRepositoryService,
		gitRepositoryCache:    libgitrepository.NewGitRepositoryCache(domainCache),
		events:                eventRecorder,
		log:                   log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *GitRepositoryDomain) GVK() schema.GroupVersionKind {
	return sourcesv1alpha1.GroupVersion.WithKind("GitRepository")
}

// Handle executes command.Command operations routed by the Engine.
func (d *GitRepositoryDomain) Handle(ctx context.Context, cmd command.Command) error {

	gitrepositoryCR, ok := cmd.Obj.(*sourcesv1alpha1.GitRepository)
	if !ok || gitrepositoryCR == nil {
		return fmt.Errorf("invalid object passed to GitRepositoryDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "gitrepository", "name", gitrepositoryCR.Name, "namespace", gitrepositoryCR.Namespace)
	log.Info("handling gitrepository command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(gitrepositoryCR)
	gen := gitrepositoryCR.GetGeneration()

	switch cmd.Type {
	case command.CmdCreate, command.CmdUpdate:

		// --------------------------------------------------------------
		// 0. Resolve GitRepository contract
		// --------------------------------------------------------------
		log.Info("resolving gitrepository contract")
		resolved, err := gitrepoResolution.ResolveGitRepository(gitrepositoryCR)
		if err != nil {
			log.Error(err, "gitrepository resolution failed")
			d.events.FromError(gitrepositoryCR, "GitRepositoryResolveFailed", err)
			conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryResolveFailed", conditions.ConditionFalse, "GitRepositoryResolve", err.Error())
			return err
		}

		// ------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		// ------------------------------------------------
		if cerr := d.gitRepositoryCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
			d.events.FromError(gitrepositoryCR, "GitRepositoryCacheFailed", cerr)
			conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryCacheFailed", conditions.ConditionFalse, "GitRepositoryCache", cerr.Error())
		}

		log.Info("gitrepository resolved successfully")
		d.events.Normal(gitrepositoryCR, "GitRepositoryResolved", "GitRepository specification resolved successfully")
		conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryResolved", conditions.ConditionTrue, "GitRepositoryResolved", "GitRepository specification resolved successfully")

		log.Info("gitrepository cached successfully")
		d.events.Normal(gitrepositoryCR, "GitRepositoryCached", "GitRepository specification cached successfully")
		conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryCached", conditions.ConditionTrue, "GitRepositorySpecCached", "GitRepository specification cached successfully")

		// ------------------------------------------------
		// 2. Ensure prerequisites (secrets, etc.)
		// ------------------------------------------------
		log.Info("creating gitrepository prerequisites")
		if err := d.gitRepositoryMediator.EnsurePrerequisites(ctx, resolved); err != nil {
			log.Error(err, "gitrepository prerequisites failed")
			d.events.FromError(gitrepositoryCR, "GitRepositoryPrerequisitesCreateFailed", err)
			conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryPrerequisitesCreateFailed", conditions.ConditionFalse, "GitRepositoryPrerequisitesCreateFailed", err.Error())
			return err
		}

		log.Info("gitrepository prerequisites ensured")
		d.events.Normal(gitrepositoryCR, "GitRepositoryPrerequisitesReady", "all gitrepository prerequisites created successfully")
		conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryPrerequisitesReady", conditions.ConditionTrue, "GitRepositoryPrerequisitesReady", "All gitrepository prerequisites satisfied")

		// ---------------------------------------------------------
		// 3. Reconcile declarative intent (service)
		// ---------------------------------------------------------
		log.Info("triggering gitrepository execution")
		if err := d.gitRepositoryService.Reconcile(ctx, resolved); err != nil {
			log.Error(err, "gitrepository triggering failed")
			d.events.FromError(gitrepositoryCR, "GitRepositoryReconcileFailed", err)
			conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryTriggered", conditions.ConditionFalse, "TriggerFailed", err.Error())
			return err
		}

		// ------------------------------------------------
		// 4. GitRepository Execution
		// ------------------------------------------------
		log.Info("gitrepository run started")
		d.events.Normal(gitrepositoryCR, "GitRepositoryRunStarted", "GitRepository run has started")
		conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryStart", conditions.ConditionTrue, "GitRepositoryRunStarted", "GitRepository run has started")
		log.Info("gitrepository domain handling complete")

	case command.CmdDelete:
		// --------------------------------------------------------
		// Real teardown, gated by finalizer at the controller level.
		// Handle() must return nil ONLY if it is safe for the
		// controller to remove the finalizer and let K8s finish
		// deleting the object. Any error here keeps the finalizer
		// in place and the controller will retry on next reconcile.
		// --------------------------------------------------------
		log.Info("gitrepository teardown requested")

		resolved, err := gitrepoResolution.ResolveGitRepository(gitrepositoryCR)
		if err != nil {
			log.Error(err, "resolution failed during teardown")
			d.events.FromError(gitrepositoryCR, "GitRepositoryTeardownResolveFailed", err)
			conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryDeleted", conditions.ConditionFalse, "GitRepositoryTeardownResolveFailed", err.Error())
			return err
		}

		// Tear down prerequisites the mediator created (secrets, SAs, RBAC).
		if err := d.gitRepositoryMediator.CleanupPrerequisites(ctx, resolved); err != nil {
			log.Error(err, "prerequisites cleanup failed")
			d.events.FromError(gitrepositoryCR, "GitRepositoryPrerequisitesCleanupFailed", err)
			conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryDeleted", conditions.ConditionFalse, "GitRepositoryPrerequisitesCleanupFailed", err.Error())
			return err
		}

		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.gitRepositoryCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}

		log.Info("gitrepository teardown complete")
		d.events.Normal(gitrepositoryCR, "GitRepositoryDeleted", "gitrepository and owned resources cleaned up successfully")
		conditions.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryDeleted", conditions.ConditionTrue, "GitRepositoryCleanupComplete", "gitrepository and owned resources cleaned up successfully")
	}

	return nil
}

// -----------------------------------------------------------------------------
// Predicate hooks
// -----------------------------------------------------------------------------

// CanCreate reports whether the supplied object can be processed as a GitRepository create operation.
func (d *GitRepositoryDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*sourcesv1alpha1.GitRepository)
	return ok
}

// CanUpdate reports whether the supplied update should trigger GitRepository reconciliation
// by comparing the specifications of the old and new objects.
func (d *GitRepositoryDomain) CanUpdate(oldObj, newObj client.Object) bool {

	oldRepo, okOld := oldObj.(*sourcesv1alpha1.GitRepository)
	newRepo, okNew := newObj.(*sourcesv1alpha1.GitRepository)

	// If either object is not a GitRepository, we cannot process the update
	if !okOld || !okNew {
		return false
	}

	// Reconcile only if spec changes
	return !reflect.DeepEqual(oldRepo.Spec, newRepo.Spec)
}

// CanDelete reports whether the supplied object can be processed as a GitRepository delete operation.
func (d *GitRepositoryDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*sourcesv1alpha1.GitRepository)
	return ok
}
