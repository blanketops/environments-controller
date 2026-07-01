/*
Copyright 2026.

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
Package build (build-observer) is the side-channel reconciler for Build CRs.
It owns two responsibilities that sit outside the main domain reconciliation
engine ("environments-build" / pkg/build/api):

  - applyTriggers: mirrors the latest matching GitHubEvent's commit metadata
    onto the Build's annotations (trigger-type/ref/sha/source), so the
    execution hash computed in pkg/build/api picks up new commits.
  - applyRetry: counts existing BuildRuns against spec.contract.policy.retry
    and bumps the retry-attempt annotation to request another dispatch when
    the most recently observed attempt has failed.

Cross-namespace note: GitHubEvent CRs live in argo-events (created by the
Argo Events Sensor); Build CRs live in default (or wherever GitOps places
them). Both List calls below are deliberately namespace-unscoped — they rely
on the environments.blanketops.dev/name label alone to correlate across
namespaces. Do not add client.InNamespace(...) back to either query; doing
so silently breaks the cross-namespace correlation (this happened once
already — see commit history).

Race note: applyRetry only acts when build.Status.Contract.ExecutionRef
matches the most recently created BuildRun. status.Contract is written
asynchronously by buildrun-observer and can still reflect a prior attempt's
outcome for a short window after a new BuildRun is dispatched but before it
completes. Without this check, applyRetry can race ahead and dispatch a
second retry before the first has even run.
*/
package build

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	buildv1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/BlanketOps/environments-api/api/events/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/build/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/build/domain"
	buildresolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	githubeventresolution "github.com/ntlaletsi70/blanketops-environments/resolution/githubevent"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	triggerTypeAnnotation   = "build.blanketops.dev/trigger-type"
	triggerRefAnnotation    = "build.blanketops.dev/trigger-ref"
	triggerSHAAnnotation    = "build.blanketops.dev/trigger-sha"
	triggerSourceAnnotation = "build.blanketops.dev/trigger-source"
	retryAttemptAnnotation  = "build.blanketops.dev/retry-attempt"
)

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *core.EventRecorder
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "build-observer", "build", req.String())
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

// applyTriggers mirrors the latest matching GitHubEvent's commit metadata
// onto the Build's annotations. It patches whenever the resolved commit SHA
// differs from what's already recorded — not just when the annotation is
// unset — so repeated pushes (e.g. from a webhook test loop) keep updating
// the Build instead of only ever capturing the first one.
func (r *Reconciler) applyTriggers(ctx context.Context, build *buildv1.Build, resolved *buildresolution.ResolvedBuild) error {
	log := ctrl.LoggerFrom(ctx).WithValues("fn", "applyTriggers")

	if len(resolved.Spec.Policy.Triggers) == 0 {
		log.Info("trigger scan: no triggers configured on policy, skipping")
		return nil
	}

	appName := build.Labels["environments.blanketops.dev/name"]
	if appName == "" {
		log.Info("trigger scan: build has no environments.blanketops.dev/name label, skipping")
		return nil
	}

	// Deliberately no client.InNamespace(...) — GitHubEvents live in a
	// different namespace (argo-events) than Build CRs. Correlate by label
	// only. See package doc.
	var events eventsv1alpha1.GitHubEventList
	if err := r.List(ctx, &events,
		client.MatchingLabels{"environments.blanketops.dev/name": appName},
	); err != nil {
		return err
	}

	log.Info("trigger scan: candidates found", "appName", appName, "count", len(events.Items))

	var latestEvent *eventsv1alpha1.GitHubEvent
	var latestResolved *githubeventresolution.ResolvedGitHubEvent

	for i := range events.Items {
		ev := &events.Items[i]
		ghResolved, err := githubeventresolution.ResolveGitHubEvent(ev)
		if err != nil {
			log.Info("trigger scan: resolve failed", "event", ev.Name, "error", err.Error())
			continue
		}

		contract := ghResolved.Spec.ToGitHubEventContract()
		if contract.GetEventId() == "" {
			log.Info("trigger scan: empty event id, skipping", "event", ev.Name)
			continue
		}

		eventType := normalizeEventType(contract.GetEventType().String())
		if !triggerMatches([]buildresolution.ResolvedBuildPolicy{*resolved.Spec.Policy}, eventType) {
			log.Info("trigger scan: event type did not match policy triggers", "event", ev.Name, "eventType", eventType)
			continue
		}

		log.Info("trigger scan: candidate matched", "event", ev.Name, "eventType", eventType, "sha", contract.GetCommitSha())

		if latestEvent == nil || ev.CreationTimestamp.After(latestEvent.CreationTimestamp.Time) {
			latestEvent = ev
			latestResolved = ghResolved
		}
	}

	if latestResolved == nil {
		log.Info("trigger scan: no matching event survived filtering, skipping")
		return nil
	}

	contract := latestResolved.Spec.ToGitHubEventContract()
	newSHA := contract.GetCommitSha()

	// FIXED: never overwrite a valid annotation with an empty SHA.
	if newSHA == "" {
		log.Info("trigger scan: event has no commit sha yet, skipping")
		return nil
	}

	// Only patch on an actual change in commit SHA — avoids a redundant
	// write (and a redundant wakeup of environments-build) every time this
	// reconciler runs against an already-current Build.
	if build.Annotations[triggerSHAAnnotation] == newSHA {
		log.Info("trigger scan: sha already current, skipping patch", "sha", newSHA)
		return nil
	}

	log.Info("trigger scan: patching build annotations", "event", latestEvent.Name, "sha", newSHA)

	original := build.DeepCopy()
	if build.Annotations == nil {
		build.Annotations = map[string]string{}
	}
	build.Annotations[triggerTypeAnnotation] = normalizeEventType(contract.GetEventType().String())
	build.Annotations[triggerRefAnnotation] = contract.GetRef()
	build.Annotations[triggerSHAAnnotation] = newSHA
	build.Annotations[triggerSourceAnnotation] = "github"

	return r.Patch(ctx, build, client.MergeFrom(original))
}

// applyRetry counts existing BuildRuns against the retry policy and bumps
// retry-attempt to request another dispatch when the most recently observed
// attempt failed. See package doc for the race it guards against.
func (r *Reconciler) applyRetry(ctx context.Context, build *buildv1.Build, resolved *buildresolution.ResolvedBuild) error {
	if resolved.Spec.Policy == nil || resolved.Spec.Policy.Retry == nil || !resolved.Spec.Policy.Retry.OnFailure {
		return nil
	}

	var current domain.BuildStatus
	if len(build.Status.Contract.Raw) > 0 {
		_ = json.Unmarshal(build.Status.Contract.Raw, &current)
	}

	if current.Success {
		// Build succeeded — clear any leftover retry bookkeeping rather than
		// leaving a stale attempt count sitting on the object indefinitely.
		if build.Annotations[retryAttemptAnnotation] != "" {
			original := build.DeepCopy()
			delete(build.Annotations, retryAttemptAnnotation)
			return r.Patch(ctx, build, client.MergeFrom(original))
		}
		return nil
	}

	if !current.Triggered {
		return nil
	}

	var runs shipwrightv1alpha1.BuildRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(build.Namespace),
		client.MatchingLabels{"build.blanketops.dev/name": build.Name},
	); err != nil {
		return err
	}

	latest := mostRecentBuildRun(runs.Items)
	if latest == nil || current.ExecutionRef != latest.Name {
		// status.Contract doesn't correspond to the most recent BuildRun yet
		// — a dispatch is already in flight and hasn't been observed as
		// terminal. Don't race ahead of it by bumping the attempt count
		// again.
		return nil
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

// mostRecentBuildRun returns the BuildRun with the latest CreationTimestamp,
// or nil if items is empty.
func mostRecentBuildRun(items []shipwrightv1alpha1.BuildRun) *shipwrightv1alpha1.BuildRun {
	var latest *shipwrightv1alpha1.BuildRun
	for i := range items {
		if latest == nil || items[i].CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = &items[i]
		}
	}
	return latest
}

// normalizeEventType converts the proto text-marshaled form of
// GetEventType() (e.g. "type:GIT_HUB_EVENT_TYPE_PUSH") into the lowercase,
// underscore form used by Build policy triggers (e.g. "push"). GetEventType
// returns a wrapper message rather than the bare enum, so calling .String()
// on it text-marshals the whole message instead of returning the enum
// constant name — this strips that down to a value triggerMatches can
// actually compare against.
func normalizeEventType(raw string) string {
	raw = strings.TrimPrefix(raw, "type:")
	raw = strings.TrimPrefix(raw, "GIT_HUB_EVENT_TYPE_")
	return strings.ToLower(raw)
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

// mapGitHubEventToBuilds wakes build-observer when a matching GitHubEvent
// changes. Deliberately no client.InNamespace(...) — see package doc.
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
