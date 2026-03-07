package sources

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	sourcesv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/sources/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/gitrepository"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/gitrepository/application"
	gitrepoResolution "github.com/ntlaletsi70/blanketops-environments/resolution/gitrepository"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GitRepositoryDomain handles GitRepository CRs.
type GitRepositoryDomain struct {
	Mediator *gitrepository.Mediator
	Service  *application.GitRepositoryService
	cache    *core.Cache
	events   *core.EventRecorder
	log      logr.Logger
}

// New constructs a new GitRepositoryDomain.
func New(mediator *gitrepository.Mediator, service *application.GitRepositoryService, cache *core.Cache, events *core.EventRecorder, log logr.Logger) *GitRepositoryDomain {
	return &GitRepositoryDomain{
		Mediator: mediator,
		Service:  service,
		cache:    cache,
		events:   events,
		log:      log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *GitRepositoryDomain) GVK() schema.GroupVersionKind {
	return sourcesv1alpha1.GroupVersion.WithKind("GitRepository")
}

// Handle processes Create / Update / Delete commands.
func (d *GitRepositoryDomain) Handle(ctx context.Context, cmd core.Command) error {

	gitrepositoryCR, ok := cmd.Obj.(*sourcesv1alpha1.GitRepository)
	if !ok || gitrepositoryCR == nil {
		return fmt.Errorf("invalid object passed to GitRepositoryDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "gitrepository", "name", gitrepositoryCR.Name, "namespace", gitrepositoryCR.Namespace)
	log.Info("handling gitrepository command", "type", cmd.Type)

	// ------------------------------------------------
	// 1. Resolve GitRepository ONCE (domain-owned)
	// ------------------------------------------------

	log.Info("resolving gitrepository contract")
	resolved, err := gitrepoResolution.ResolveGitRepository(gitrepositoryCR)

	if err != nil {

		log.Error(err, "gitrepository resolution failed")
		d.events.FromError(gitrepositoryCR, "GitRepositoryResolveFailed", err)
		core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryResolved", core.ConditionFalse, "InvalidSpec", err.Error())

		return err
	}

	log.Info("gitrepository resolved successfully")
	d.events.Normal(gitrepositoryCR, "GitRepositoryResolved", "GitRepository specification resolved successfully")
	core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryResolved", core.ConditionTrue, "Resolved", "GitRepository specification resolved successfully")

	// ------------------------------------------------
	// 2. Ensure prerequisites (secrets, etc.)
	// ------------------------------------------------

	log.Info("ensuring gitrepository prerequisites")

	if err := d.Mediator.EnsurePrerequisites(ctx, resolved); err != nil {

		log.Error(err, "gitrepository prerequisites failed")
		d.events.FromError(gitrepositoryCR, "GitRepositoryPrerequisitesFailed", err)
		core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryPrerequisitesReady", core.ConditionFalse, "GitRepositoryPrerequisitesFailed", err.Error())

		return err
	}

	log.Info("gitrepository prerequisites ensured")
	d.events.Normal(gitrepositoryCR, "GitRepositoryPrerequisitesReady", "All gitrepository prerequisites created successfully")
	core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryPrerequisitesReady", core.ConditionTrue, "GitRepositoryPrerequisitesReady", "All gitrepository prerequisites satisfied")

	// ------------------------------------------------
	// 3. Reconcile declarative intent (service)
	// ------------------------------------------------

	log.Info("triggering gitrepository execution")

	if err := d.Service.Reconcile(ctx, resolved); err != nil {

		log.Error(err, "gitrepository trigger failed")
		d.events.FromError(gitrepositoryCR, "GitRepositoryReconcileFailed", err)
		core.SetCondition(&gitrepositoryCR.Status.Conditions, "GitRepositoryTriggered", core.ConditionFalse, "GitRepositoryTriggerdFailed", err.Error())

		return err
	}

	return nil
}

// CanCreate determines whether this domain handles create events.
func (d *GitRepositoryDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*sourcesv1alpha1.GitRepository)
	return ok
}

// CanUpdate determines whether updates should trigger reconciliation.
func (d *GitRepositoryDomain) CanUpdate(
	oldObj, newObj client.Object,
) bool {
	oldRepo, okOld := oldObj.(*sourcesv1alpha1.GitRepository)
	newRepo, okNew := newObj.(*sourcesv1alpha1.GitRepository)
	if !okOld || !okNew {
		return false
	}
	if !okOld || !okNew {
		return false
	}

	// Reconcile only if spec changes
	return !reflect.DeepEqual(oldRepo.Spec, newRepo.Spec)
}

// CanDelete determines whether delete events are handled.
func (d *GitRepositoryDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*sourcesv1alpha1.GitRepository)
	return ok
}
