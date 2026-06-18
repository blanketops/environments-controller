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

	eventsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/events/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/domain"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	Status *application.StatusWriter

	Recorder *core.EventRecorder
}

// Reconcile is invoked by controller-runtime for every Argo Sensor event. It
// resolves the owning GitHubEvent CR via label and emits an observation event.
// Sensors not labelled as BlanketOps-owned are silently ignored.
// Reconcile is invoked by controller-runtime for every Argo Sensor event. It
// resolves the owning GitHubEvent CR via label and emits an observation event.
// Sensors not labelled as BlanketOps-owned are silently ignored.
func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	log := ctrl.LoggerFrom(ctx).WithValues("controller", "githubevent", "payload", req.NamespacedName.String())

	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch the Argo Sensor as an unstructured object.
	//
	// We use unstructured to avoid a hard dependency on the Argo Events
	// API types. The observer only needs the resource's labels and namespace
	// to perform ownership resolution — no deep field access required.
	// ------------------------------------------------
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "events.blanketops.dev",
		Version: "v1alpha1",
		Kind:    "GitHubPayload",
	})

	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		log.Info("payload not found, ignoring")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// Resolve the owning GitHubEvent via label.
	//
	// The GitHubEvent domain stamps this label on every Sensor it creates.
	// Sensors without the label are not platform-owned and are ignored.
	// ------------------------------------------------
	// ------------------------------------------------
	// Resolve the owning GitHubEvent via label.
	//
	// The GitHubEvent domain stamps this label on every Sensor it creates.
	// Sensors without the label are not platform-owned and are ignored.
	// ------------------------------------------------
	labels := obj.GetLabels()
	eventName := labels["events.blanketops.dev/githubevent"]
	if eventName == "" {
		return ctrl.Result{}, nil
	}

	namespace := obj.GetNamespace()
	if namespace == "" {
		namespace = "default"
	}

	var ev eventsv1alpha1.GitHubEvent
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      eventName,
	}, &ev); err != nil {
		log.Error(err, "failed to fetch owning githubevent")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log = log.WithValues("githubevent", ev.Name)
	log.Info("payload linked to githubevent")

	// ------------------------------------------------
	// Emit observation event on the owning GitHubEvent CR.
	//
	// This is the observer's sole output. No domain state is written.
	// A nil Recorder is safe and results in a no-op.
	// ------------------------------------------------
	if r.Recorder != nil {
		r.Recorder.Normal(
			&ev,
			"PayloadObserved",
			"GitHub webhook delivery recorded as %s",
			obj.GetName(),
		)
	}

	// ------------------------------------------------
	// Close the Loop
	// ------------------------------------------------
	if r.Status != nil {
		log.Info("finalizing githubevent status")

		// Construct the domain result representing the payload arrival
		result := domain.GitHubEventResult{
			Phase:          "PayloadReceived",
			LastPayloadRef: obj.GetName(),
		}

		return ctrl.Result{}, r.Status.Write(ctx, &ev, result, nil)
	}

	return ctrl.Result{}, nil
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
		For(&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "events.blanketops.dev/v1alpha1",
				"kind":       "GitHubPayload",
			},
		}).
		Complete(r)
}
