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

	"github.com/go-logr/logr"
	sourcesv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/sources/v1alpha1"
	libgitrepository "github.com/ntlaletsi70/blanketops-environments/cache/gitrepository"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/gitrepository/application"
	gitrepoResolution "github.com/ntlaletsi70/blanketops-environments/resolution/gitrepository"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/gitrepository"
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
	events *core.EventRecorder
	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new GitRepositoryDomain instance configured with the necessary dependencies.
func New(mediator *gitrepository.Mediator, service *application.GitRepositoryService, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *GitRepositoryDomain {
	return &GitRepositoryDomain{
		gitRepositoryMediator: mediator,
		gitRepositoryService:  service,
		gitRepositoryCache:    libgitrepository.NewGitRepositoryCache(cache),
		events:                events,
		log:                   log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *GitRepositoryDomain) GVK() schema.GroupVersionKind {
	return sourcesv1alpha1.GroupVersion.WithKind("GitRepository")
}

// Handle executes core.Command operations routed by the Engine.
func (d *GitRepositoryDomain) Handle(ctx context.Context, cmd core.Command) error {

	gitrepositoryCR, ok := cmd.Obj.(*sourcesv1alpha1.GitRepository)
	if !ok || gitrepositoryCR == nil {
		return fmt.Errorf("invalid object passed to GitRepositoryDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "gitrepository", "name", gitrepositoryCR.Name, "namespace", gitrepositoryCR.Namespace)
	log.Info("handling gitrepository command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(gitrepositoryCR)
	gen := gitrepositoryCR.GetGeneration()

	switch cmd.Type {
	case core.CmdCreate, core.CmdUpdate:

		// --------------------------------------------------------------
		// 0. Resolve GitRepository ONCE (domain-owned)
		// --------------------------------------------------------------
		log.Info("resolving gitrepository contract")
		resolved, err := gitrepoResolution.ResolveGitRepository(gitrepositoryCR)

		if err != nil {

			log.Error(err, "gitrepository resolution failed")
			d.events.FromError(gitrepositoryCR, "GitRepositoryResolveFailed", err)
			core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryResolved", core.ConditionFalse, "InvalidSpec", err.Error())

			return err
		}

		// ------------------------------------------------
		// Stage 1: Publish resolved contract to cache for observability and potential reuse within the same generation.
		// ------------------------------------------------
		if cerr := d.gitRepositoryCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
		}

		log.Info("gitrepository resolved successfully")
		d.events.Normal(gitrepositoryCR, "GitRepositoryResolved", "GitRepository specification resolved successfully")
		core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryResolved", core.ConditionTrue, "Resolved", "GitRepository specification resolved successfully")

		// ------------------------------------------------
		// 2. Ensure prerequisites (secrets, etc.)
		// ------------------------------------------------
		log.Info("ensuring gitrepository prerequisites")

		if err := d.gitRepositoryMediator.EnsurePrerequisites(ctx, resolved); err != nil {

			log.Error(err, "gitrepository prerequisites failed")
			d.events.FromError(gitrepositoryCR, "GitRepositoryPrerequisitesFailed", err)
			core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryPrerequisitesReady", core.ConditionFalse, "GitRepositoryPrerequisitesFailed", err.Error())

			return err
		}

		log.Info("gitrepository prerequisites ensured")
		d.events.Normal(gitrepositoryCR, "GitRepositoryPrerequisitesReady", "all gitrepository prerequisites created successfully")
		core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryPrerequisitesReady", core.ConditionTrue, "GitRepositoryPrerequisitesReady", "All gitrepository prerequisites satisfied")

		// ---------------------------------------------------------
		// 3. Reconcile declarative intent (service)
		// ---------------------------------------------------------
		log.Info("triggering gitrepository execution")

		if err := d.gitRepositoryService.Reconcile(ctx, resolved); err != nil {

			log.Error(err, "gitrepository triggering failed")
			d.events.FromError(gitrepositoryCR, "GitRepositoryReconcileFailed", err)
			core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryTriggered", core.ConditionFalse, "TriggerFailed", err.Error())

			return err
		}

		// ------------------------------------------------
		// 4. GitRepository Execution completed
		// ------------------------------------------------

		log.Info("gitrepository execution requested")
		d.events.Normal(gitrepositoryCR, "GitRepositoryTriggered", "GitRepository execution has started")
		core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryTriggered", core.ConditionTrue, "ExecutionStarted", "GitRepository execution has started")
		log.Info("gitrepository domain handling complete")

	case core.CmdDelete:
		// Drop the projection for this object (all generations). No-op on
		// backends without key enumeration; generation scoping + TTL
		// covers correctness there.
		if cerr := d.gitRepositoryCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}
		d.events.Info(gitrepositoryCR, "GitRepositoryDeleted", "gitrepository cleanup not implemented yet")
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
