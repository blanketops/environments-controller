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
Package githubevent implements the GitHubEvent observer.

The GitHubEvent observer bridges the Argo Events layer and the BlanketOps
event model. It watches Argo Sensor resources — the external objects that
Argo Events creates and drives in response to incoming GitHub webhook payloads
— and surfaces their activity as Kubernetes events on the owning GitHubEvent CR.

Responsibility boundary:
  - Watch Argo Sensor resources for state changes.
  - Resolve the owning GitHubEvent CR via the events.blanketops.dev/githubevent label.
  - Emit a Kubernetes event on the GitHubEvent CR to record external activity.
  - MUST NOT mutate domain state. This observer is read-only with respect to
    the BlanketOps resource model.

Architecture note:
The GitHubEvent CR owns the Argo EventSource, EventBus, and Sensor as child
resources — they are created and managed by the GitHubEvent controller (domain
layer), not by this observer. This observer exists solely to reflect activity
on those child resources back up to the parent GitHubEvent CR as observable
events, completing the feedback loop without crossing ownership boundaries.
*/
package githubevent

import (
	"context"

	argoeventsv1alpha1 "github.com/argoproj/argo-events/pkg/apis/events/v1alpha1"
	eventsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/events/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/domain"
	githubeventresolution "github.com/ntlaletsi70/blanketops-environments/resolution/githubevent"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler observes Argo Sensor resources on behalf of the GitHubEvent domain.
// It MUST NOT mutate domain state — its only output is Kubernetes events emitted
// on the owning GitHubEvent CR.
// Reconciler observes Argo Sensor resources on behalf of the GitHubEvent domain.
// It MUST NOT mutate domain state — its only output is Kubernetes events emitted
// on the owning GitHubEvent CR.
type Reconciler struct {
	client.Client
	// Recorder emits Kubernetes events on the owning GitHubEvent resource.
	Status   *application.StatusWriter
	Recorder *core.EventRecorder
}

// Reconcile is invoked by controller-runtime for every Argo Sensor event. It
// resolves the owning GitHubEvent CR via label and emits an observation event.
// Sensors not labelled as BlanketOps-owned are silently ignored.
// Reconcile is invoked by controller-runtime for every Argo Sensor event. It
// resolves the owning GitHubEvent CR via label and emits an observation event.
// Sensors not labelled as BlanketOps-owned are silently ignored.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := ctrl.LoggerFrom(ctx).WithValues("controller", "githubevent-observer", "payload", req.NamespacedName.String())
	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch Sensor
	// ------------------------------------------------
	var gh argoeventsv1alpha1.Sensor
	if err := r.Get(ctx, req.NamespacedName, &gh); err != nil {
		log.Info("argo events sensor not found, ignoring")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// Only act on terminal Sensor Payloads
	// ------------------------------------------------
	cond := gh.Status.GetCondition("Succeeded")
	if cond == nil || cond.Status == corev1.ConditionUnknown {
		log.Info("skipping: sensor payload not terminal yet")
		return ctrl.Result{}, nil
	}

	success := cond.Status == corev1.ConditionTrue
	log = log.WithValues("succeeded", success, "reason", cond.Reason)

	// ------------------------------------------------
	// Resolve owning GitHubEvent
	// ------------------------------------------------
	githubEventName := gh.Labels["events.blanketops.dev/githubevent"]
	if githubEventName == "" {
		log.Info("skipping: sensor payload has no owning build label")
		return ctrl.Result{}, nil
	}

	var githubevent eventsv1alpha1.GitHubEvent
	if err := r.Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: githubEventName}, &githubevent); err != nil {
		log.Error(err, "failed to fetch owning githubevent")
		return ctrl.Result{}, err
	}

	log = log.WithValues("githubevent", githubevent.Name, "namespace", githubevent.Namespace)
	// ------------------------------------------------
	// Resolve runtime Build (AUTHORITATIVE)
	// ------------------------------------------------
	_, err := githubeventresolution.ResolveGitHubEvent(&githubevent)
	if err != nil {
		log.Error(err, "failed to resolve githubvent contract")
		return ctrl.Result{}, err
	}

	log.Info("githubevent payload recieved")
	// ------------------------------------------------
	// Emit events (terminal only)
	// ------------------------------------------------
	if r.Recorder != nil {
		if success {
			r.Recorder.Normal(
				&githubevent,
				"GitHubEventProcess",
				"GitHubEventProcess %s completed successfully",
				gh.Name,
			)
		} else {
			r.Recorder.Warn(
				&githubevent,
				"GitHubEventProcess",
				"GitHubEventProcess %s failed: %s",
				gh.Name,
				cond.Message,
			)
		}
	}

	log.Info("finalizing githubevent status")

	result := domain.GitHubEventResult{
		Success:         success,
		Message:         cond.Message,
		PayloadRecieved: true,
	}

	return ctrl.Result{}, r.Status.Write(ctx, &githubevent, result, nil)
}

// SetupWithManager registers the GitHubEvent observer with the controller manager.
//
// The observer watches Argo Sensor resources rather than GitHubEvent CRs.
// This is intentional — the Sensor is the external object whose state changes
// indicate GitHub webhook activity. Watching the GitHubEvent CR itself would
// give us no signal about what the external system is doing.
//
// Unstructured is used here to avoid importing the Argo Events API types as
// a hard dependency. The manager uses the GVK embedded in the object to
// establish the correct informer watch.
// SetupWithManager registers the GitHubEvent observer with the controller manager.
//
// The observer watches Argo Sensor resources rather than GitHubEvent CRs.
// This is intentional — the Sensor is the external object whose state changes
// indicate GitHub webhook activity. Watching the GitHubEvent CR itself would
// give us no signal about what the external system is doing.
//
// Unstructured is used here to avoid importing the Argo Events API types as
// a hard dependency. The manager uses the GVK embedded in the object to
// establish the correct informer watch.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("githubevent-observer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&argoeventsv1alpha1.Sensor{}).
		Complete(r)
}
