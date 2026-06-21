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
	"encoding/json"

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
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "githubevent-observer", "githubevent", req.NamespacedName.String())
	log.Info("reconcile start")

	var githubevent eventsv1alpha1.GitHubEvent
	if err := r.Get(ctx, req.NamespacedName, &githubevent); err != nil {
		log.Info("githubevent not found, ignoring")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// Resolve runtime GitHubEvent (AUTHORITATIVE)
	// ------------------------------------------------
	resolved, err := githubeventresolution.ResolveGitHubEvent(&githubevent)
	if err != nil {
		log.Error(err, "failed to resolve githubevent contract")
		return ctrl.Result{}, err
	}

	// ------------------------------------------------
	// Truth: payload arrived if contract has event data
	// ------------------------------------------------
	contract := resolved.Spec.ToGitHubEventContract()
	payloadReceived := contract.GetEventId() != "" && contract.GetEventType() != nil
	log = log.WithValues("payloadReceived", payloadReceived)
	log.Info("githubevent resolved")

	// If no payload yet, nothing to do
	if !payloadReceived {
		log.Info("no payload yet, skipping")
		return ctrl.Result{}, nil
	}

	// ------------------------------------------------
	// Preserve Triggered/Success from existing status
	// The controller set these during provisioning.
	// ------------------------------------------------
	var existingStatus domain.GitHubEventStatus
	triggered := false
	success := false
	if len(githubevent.Status.Contract.Raw) > 0 {
		if err := json.Unmarshal(githubevent.Status.Contract.Raw, &existingStatus); err == nil {
			triggered = existingStatus.Triggered
			success = existingStatus.Success
			log.Info("preserved existing status values", "triggered", triggered, "success", success)
		}
	}

	// ------------------------------------------------
	// Check Sensor health for Success (best effort, don't overwrite)
	// Only update success if sensor is actually healthy
	// ------------------------------------------------
	var sensor argoeventsv1alpha1.Sensor
	sensorName := "github-sensor-" + githubevent.Name
	if err := r.Get(ctx, client.ObjectKey{Namespace: githubevent.Namespace, Name: sensorName}, &sensor); err != nil {
		log.Error(err, "failed to fetch sensor", "sensor", sensorName)
	} else if cond := sensor.Status.GetCondition("Succeeded"); cond != nil {
		if cond.Status == corev1.ConditionTrue {
			success = true
		}
		log = log.WithValues("sensorSucceeded", cond.Status == corev1.ConditionTrue, "reason", cond.Reason)
	}

	// ------------------------------------------------
	// Emit events
	// ------------------------------------------------
	if r.Recorder != nil {
		if payloadReceived {
			if success {
				r.Recorder.Normal(&githubevent, "GitHubEventPayloadReceived", "Payload received for %s, sensor healthy", sensorName)
			} else {
				r.Recorder.Warn(&githubevent, "GitHubEventPayloadReceived", "Payload received for %s, sensor condition: %s", sensorName, "unknown")
			}
		}
	}

	log.Info("finalizing githubevent status")

	// ------------------------------------------------
	// Finalize status — preserve controller's Triggered, update Success
	// ------------------------------------------------
	result := domain.GitHubEventResult{
		Success:         success,
		Message:         "Payload received",
		Triggered:       triggered, // PRESERVED from controller
		PayloadRecieved: payloadReceived,
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
	r.Status = application.NewStatusWriter(mgr.GetClient(), ctrl.Log.WithName("githubevent-status-writer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&eventsv1alpha1.GitHubEvent{}).
		Complete(r)
}
