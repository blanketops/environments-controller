package githubevent

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	eventsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/events/v1alpha1"

	"github.com/ntlaletsi70/blanketops-environments/core"

	githubeventMediator "github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/githubevent"
	"github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/application"
	githubeventResolution "github.com/ntlaletsi70/blanketops-environments/resolution/githubevent"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GitHubEventDomain handles GitHubEvent CRs.
// This represents a FACT INGESTION boundary.
type GitHubEventDomain struct {
	Service  *application.GitHubEventService
	Mediator *githubeventMediator.Mediator
	events   *core.EventRecorder
	cache    *core.Cache
	log      logr.Logger
}

func New(service *application.GitHubEventService, mediator *githubeventMediator.Mediator, events *core.EventRecorder, cache *core.Cache, log logr.Logger) *GitHubEventDomain {
	return &GitHubEventDomain{
		Service:  service,
		Mediator: mediator,
		events:   events,
		cache:    cache,
		log:      log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *GitHubEventDomain) GVK() schema.GroupVersionKind {
	return eventsv1alpha1.GroupVersion.WithKind("GitHubEvent")
}

func (d *GitHubEventDomain) Handle(ctx context.Context, cmd core.Command) error {

	githubeventCR, ok := cmd.Obj.(*eventsv1alpha1.GitHubEvent)
	if !ok || githubeventCR == nil {
		return fmt.Errorf("invalid object passed to GitHubEventDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues("domain", "githubevent", "name", githubeventCR.Name, "namespace", githubeventCR.Namespace)
	log.Info("handling githubevent command", "type", cmd.Type)

	// --------------------------------------------------------
	// 1. Resolve GitHubEvent contract ONCE
	// --------------------------------------------------------

	log.Info("resolving githubevent contract")
	resolved, err := githubeventResolution.ResolveGitHubEvent(githubeventCR)

	if err != nil {

		log.Error(err, "githubevent resolution failed")
		d.events.FromError(githubeventCR, "GitHubEventResolveFailed", err)
		core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventResolved", core.ConditionFalse, "InvalidSpec", err.Error())

		return err
	}

	log.Info("githubevent resolved successfully")
	d.events.Normal(githubeventCR, "GitHubEventResolved", "Build specification resolved successfully")
	core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventResolved", core.ConditionTrue, "GitHubEventResolved", "GitHubEvent specification resolved successfully")

	// -----------------------------------------------------------
	// 2. Ensure prerequisites (secrets, webhooks, etc.)
	// -----------------------------------------------------------
	log.Info("ensuring githubevent prerequisites")

	if err := d.Mediator.EnsurePrerequisites(ctx, resolved); err != nil {

		log.Error(err, "githubevent prerequisites failed")
		d.events.FromError(githubeventCR, "GitHubEventPrerequisitesFailed", err)
		core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventPrerequisitesReady", core.ConditionFalse, "GitHubEventPrerequisitesFailed", err.Error())

		return err
	}

	log.Info("githubevent prerequisites ensured")
	d.events.Normal(githubeventCR, "GitHubEventPrerequisitesReady", "all githubevent prerequisites created successfully")
	core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventPrerequisitesReady", core.ConditionTrue, "GitHubEventPrerequisitesReady", "All prerequisites created successfully")

	// ---------------------------------------------------------
	// 3. Domain application logic
	// ---------------------------------------------------------

	log.Info("triggering githubevent execution")

	if err := d.Service.Reconcile(ctx, resolved); err != nil {

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
	core.SetCondition(&githubeventCR.Status.Conditions, "GitHubEventOrganized", core.ConditionTrue, "EventingStarted", "GitHubEvent organization has started")
	log.Info("githubevent domain handling complete")

	return nil
}

func (d *GitHubEventDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*eventsv1alpha1.GitHubEvent)
	return ok
}

func (d *GitHubEventDomain) CanUpdate(
	oldObj, newObj client.Object,
) bool {
	oldEv, okOld := oldObj.(*eventsv1alpha1.GitHubEvent)
	newEv, okNew := newObj.(*eventsv1alpha1.GitHubEvent)
	if !okOld || !okNew {
		return false
	}

	// GitHubEvent is immutable: reconcile ONLY if spec changed
	return !reflect.DeepEqual(oldEv.Spec, newEv.Spec)
}

func (d *GitHubEventDomain) CanDelete(obj client.Object) bool {
	// Events are historical facts; nothing to undo
	return false
}
