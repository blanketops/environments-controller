package build

import (
	"context"
	"encoding/json"
	"strconv"

	buildv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/events/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	buildresolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	githubeventresolution "github.com/ntlaletsi70/blanketops-environments/resolution/githubevent"
	shipwrightv1beta1 "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const retryAttemptAnnotation = "build.blanketops.dev/retry-attempt"

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *core.EventRecorder
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "build-observer", "build", req.NamespacedName.String())
	log.Info("reconcile start")

	var build buildv1.Build
	if err := r.Get(ctx, req.NamespacedName, &build); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	resolved, err := buildresolution.ResolveBuild(&build)
	if err != nil {
		log.Error(err, "failed to resolve build contract")
		return ctrl.Result{}, err
	}

	if err := r.applyTriggers(ctx, &build, resolved); err != nil {
		log.Error(err, "failed to apply triggers")
		return ctrl.Result{}, err
	}

	if err := r.applyRetry(ctx, &build, resolved); err != nil {
		log.Error(err, "failed to apply retry")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) applyTriggers(ctx context.Context, build *buildv1.Build, resolved *buildresolution.ResolvedBuild) error {
	if len(resolved.Spec.Policy.Triggers) == 0 {
		return nil
	}

	if build.Annotations["build.blanketops.dev/trigger-sha"] != "" {
		return nil
	}

	appName := build.Labels["environments.blanketops.dev/name"]
	if appName == "" {
		return nil
	}

	var events eventsv1alpha1.GitHubEventList
	if err := r.List(ctx, &events,
		client.InNamespace(build.Namespace),
		client.MatchingLabels{"environments.blanketops.dev/name": appName},
	); err != nil {
		return err
	}

	var latestEvent *eventsv1alpha1.GitHubEvent
	var latestResolved *githubeventresolution.ResolvedGitHubEvent

	for i := range events.Items {
		ev := &events.Items[i]
		ghResolved, err := githubeventresolution.ResolveGitHubEvent(ev)
		if err != nil {
			continue
		}

		contract := ghResolved.Spec.ToGitHubEventContract()
		if contract.GetEventId() == "" {
			continue
		}

		if !triggerMatches([]buildresolution.ResolvedBuildPolicy{*resolved.Spec.Policy}, contract.GetEventType().String()) {
			continue
		}

		if latestEvent == nil || ev.CreationTimestamp.After(latestEvent.CreationTimestamp.Time) {
			latestEvent = ev
			latestResolved = ghResolved
		}
	}

	if latestResolved == nil {
		return nil
	}

	original := build.DeepCopy()
	if build.Annotations == nil {
		build.Annotations = map[string]string{}
	}

	contract := latestResolved.Spec.ToGitHubEventContract()
	build.Annotations["build.blanketops.dev/trigger-type"] = string(contract.GetEventType().Type)
	build.Annotations["build.blanketops.dev/trigger-ref"] = contract.GetRef()
	build.Annotations["build.blanketops.dev/trigger-sha"] = contract.GetCommitSha()
	build.Annotations["build.blanketops.dev/trigger-source"] = "github"

	return r.Patch(ctx, build, client.MergeFrom(original))
}

func (r *Reconciler) applyRetry(ctx context.Context, build *buildv1.Build, resolved *buildresolution.ResolvedBuild) error {
	if resolved.Spec.Policy == nil || resolved.Spec.Policy.Retry == nil || !resolved.Spec.Policy.Retry.OnFailure {
		return nil
	}

	// Read current status from contract
	var current struct {
		Success   bool `json:"success"`
		Triggered bool `json:"triggered"`
	}
	if len(build.Status.Contract.Raw) > 0 {
		_ = json.Unmarshal(build.Status.Contract.Raw, &current)
	}

	if current.Success || !current.Triggered {
		return nil
	}

	if build.Annotations[retryAttemptAnnotation] != "" {
		return nil
	}

	var runs shipwrightv1beta1.BuildRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(build.Namespace),
		client.MatchingLabels{"build.blanketops.dev/name": build.Name},
	); err != nil {
		return err
	}

	attempts := len(runs.Items)
	if attempts >= int(resolved.Spec.Policy.Retry.MaxAttempts) {
		return nil
	}

	original := build.DeepCopy()
	if build.Annotations == nil {
		build.Annotations = map[string]string{}
	}
	build.Annotations[retryAttemptAnnotation] = strconv.Itoa(attempts + 1)

	return r.Patch(ctx, build, client.MergeFrom(original))
}

func triggerMatches(triggers []buildresolution.ResolvedBuildPolicy, eventType string) bool {
	for _, t := range triggers {
		for _, trigger := range t.Triggers {
			if trigger.Type == eventType {
				return true
			}
		}
	}
	return false
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("build-observer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&buildv1.Build{}).
		Watches(
			&eventsv1alpha1.GitHubEvent{},
			handler.EnqueueRequestsFromMapFunc(r.mapGitHubEventToBuilds),
		).
		Complete(r)
}

func (r *Reconciler) mapGitHubEventToBuilds(ctx context.Context, obj client.Object) []reconcile.Request {
	ghEvent, ok := obj.(*eventsv1alpha1.GitHubEvent)
	if !ok {
		return nil
	}

	appName := ghEvent.Labels["environments.blanketops.dev/name"]
	if appName == "" {
		return nil
	}

	var builds buildv1.BuildList
	if err := r.List(ctx, &builds,
		client.InNamespace(ghEvent.Namespace),
		client.MatchingLabels{"environments.blanketops.dev/name": appName},
	); err != nil {
		return nil
	}

	reqs := make([]reconcile.Request, 0, len(builds.Items))
	for _, b := range builds.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&b),
		})
	}
	return reqs
}
