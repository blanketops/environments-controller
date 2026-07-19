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
Package githubevent (githubevent-observer) is the status rollup reconciler
for GitHubEvent CRs.

It reflects two independent signals onto GitHubEvent.Status:

  - Payload receipt: whether the Argo Events Sensor has delivered a webhook
    payload with a non-empty event id and event type. This is the primary,
    always-available signal — it comes straight from the GitHubEvent spec
    the Sensor already wrote.
  - Sensor health: the Succeeded condition on the Sensor object the mediator
    provisioned (named "github-sensor-<event-name>"). This is best-effort —
    see the Sensor-lookup note below.

Environment prerequisite: unlike build-observer, this reconciler itself
never looks up the owning Environment. That gate lives one layer down, in
the GitHubEvent mediator's EnsurePrerequisites (internal/mediators/githubevent),
which is invoked from the domain's CmdCreate/CmdUpdate handling before this
observer ever runs. If environments.blanketops.dev/name resolves to an
Environment that doesn't exist yet, the domain reconcile fails there with
"environment %q not found — must pre-exist" — the same fail-fast gate
build's mediator enforces (see internal/mediators/build package doc). This
observer's job starts after that gate has already passed once.

Sensor-lookup note: fetching the Sensor is deliberately non-fatal. The
Sensor is provisioned by the mediator asynchronously and may not exist yet
on the very first reconcile after payload receipt, or may be transiently
unreachable. Rather than fail the whole reconcile over a supplementary
health signal, a fetch error is logged and the reconcile falls back to
whatever success value was already persisted in status — payload receipt
remains the authoritative "is this event real" signal regardless of
Sensor lookup outcome.
*/
package githubevent

import (
	"context"
	"encoding/json"
	"time"

	argoeventsv1alpha1 "github.com/argoproj/argo-events/pkg/apis/events/v1alpha1"
	eventsv1alpha1 "github.com/blanketops/environments-api/api/events/v1alpha1"
	"github.com/blanketops/environments/core"
	"github.com/blanketops/environments/pkg/apis/githubevent/application"
	"github.com/blanketops/environments/pkg/apis/githubevent/domain"
	githubeventresolution "github.com/blanketops/environments/resolution/githubevent"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// conditionGitHubEventReady is the condition type used across this file.
	// Extracted as a constant — 3 occurrences.
	conditionGitHubEventReady = "GitHubEventReady"
)

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *core.EventRecorder
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	n := req.NamespacedName
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "githubevent-observer", "githubevent", n.String())
	log.Info("reconcile start")

	var gh eventsv1alpha1.GitHubEvent
	if err := r.Get(ctx, req.NamespacedName, &gh); err != nil {
		log.Info("githubevent not found, ignoring")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	resolved, err := githubeventresolution.ResolveGitHubEvent(&gh)
	if err != nil {
		log.Error(err, "failed to resolve githubevent contract")
		return ctrl.Result{}, err
	}

	contract := resolved.Spec.ToGitHubEventContract()
	payloadReceived := contract.GetEventId() != "" && contract.GetEventType() != nil
	log = log.WithValues("payloadReceived", payloadReceived)
	log.Info("githubevent resolved")

	if !payloadReceived {
		log.Info("no payload yet, skipping")
		return ctrl.Result{}, nil
	}

	var existingStatus domain.GitHubEventStatus
	triggered := false
	success := false
	if len(gh.Status.Contract.Raw) > 0 {
		if err := json.Unmarshal(gh.Status.Contract.Raw, &existingStatus); err == nil {
			triggered = existingStatus.Triggered
			success = existingStatus.Success
			log.Info("preserved existing status", "triggered", triggered, "success", success)
		}
	}

	// Best-effort: the Sensor is provisioned asynchronously by the mediator
	// and may not exist yet, or may be transiently unreachable. A fetch
	// failure here does not fail the reconcile — see package doc's
	// Sensor-lookup note. `success` simply retains whatever value was
	// already persisted in status.
	sensorName := "github-sensor-" + gh.Name
	var sensor argoeventsv1alpha1.Sensor
	if err := r.Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: sensorName}, &sensor); err != nil {
		log.Error(err, "failed to fetch sensor", "sensor", sensorName)
	} else if cond := sensor.Status.GetCondition("Succeeded"); cond != nil {
		if cond.Status == corev1.ConditionTrue {
			success = true
		}
		log = log.WithValues("sensorSucceeded", cond.Status == corev1.ConditionTrue, "reason", cond.Reason)
	}

	if r.Recorder != nil {
		if success {
			r.Recorder.Normal(&gh, "GitHubEventPayloadReceived", "Payload received for %s, sensor healthy", sensorName)
		} else {
			r.Recorder.Warn(&gh, "GitHubEventPayloadReceived", "Payload received for %s, sensor condition unknown", sensorName)
		}
	}

	log.Info("finalizing githubevent status")

	conditions := r.buildContractAndConditions(&gh, payloadReceived, triggered, success, log)

	return ctrl.Result{}, r.Status.Write(ctx, &gh, conditions...)
}

// buildContractAndConditions derives the GitHubEventStatus contract and the
// single Condition to report from the three booleans Reconcile computed:
// payloadReceived (from spec), triggered/success (from prior persisted
// status, refined by the best-effort Sensor check above). Priority order
// mirrors the delivery lifecycle: Ready (payload + sensor healthy) beats
// Receiving (payload only) beats the prior Triggered state beats Pending.
func (r *Reconciler) buildContractAndConditions(
	gh *eventsv1alpha1.GitHubEvent,
	payloadReceived, triggered, success bool,
	log logr.Logger,
) []metav1.Condition {
	now := metav1.NewTime(time.Now())

	contractStatus := domain.GitHubEventStatus{
		Accepted:        success && payloadReceived,
		Triggered:       triggered,
		Success:         success,
		PayloadRecieved: payloadReceived,
		Message:         "Payload received",
	}

	raw, err := json.Marshal(contractStatus)
	if err != nil {
		log.Error(err, "failed to marshal contract status")
	} else {
		gh.Status.Contract = runtime.RawExtension{Raw: raw}
	}

	var condition metav1.Condition
	switch {
	case payloadReceived && success:
		condition = metav1.Condition{
			Type:               conditionGitHubEventReady,
			Status:             metav1.ConditionTrue,
			Reason:             "GitHubEventPayloadReceived",
			Message:            contractStatus.Message,
			LastTransitionTime: now,
		}
	case payloadReceived:
		condition = metav1.Condition{
			Type:               "GitHubEventReceiving",
			Status:             metav1.ConditionTrue,
			Reason:             "GitHubEventPayloadObserved",
			Message:            "Payload received, processing",
			LastTransitionTime: now,
		}
	case triggered:
		condition = metav1.Condition{
			Type:               conditionGitHubEventReady,
			Status:             metav1.ConditionTrue,
			Reason:             conditionGitHubEventReady,
			Message:            "GitHubEvent infrastructure provisioned",
			LastTransitionTime: now,
		}
	default:
		condition = metav1.Condition{
			Type:               "GitHubEventPending",
			Status:             metav1.ConditionFalse,
			Reason:             "GitHubEventPending",
			Message:            contractStatus.Message,
			LastTransitionTime: now,
		}
	}

	return []metav1.Condition{condition}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("githubevent-observer"))
	r.Status = application.NewStatusWriter(mgr.GetClient(), ctrl.Log.WithName("githubevent-status-writer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&eventsv1alpha1.GitHubEvent{}).
		Complete(r)
}
