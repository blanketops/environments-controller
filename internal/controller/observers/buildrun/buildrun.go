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
Package buildrun observes Shipwright BuildRun objects and reflects their
terminal Succeeded condition back onto the owning Build CR's status.

This exists because the Build domain's own Ensure() can only report that
build infrastructure was successfully dispatched (Triggered=true) — it
does not wait for the BuildRun to actually finish. This observer is the
other half: it watches BuildRun directly (not Build), skips non-terminal
runs, resolves the owner via the build.blanketops.dev/name label, and
writes the real success/failure outcome the Build CR's contract status
needed all along.
*/
package buildrun

import (
	"context"
	"encoding/json"
	"time"

	buildv1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	"github.com/blanketops/environments/core/events"
	"github.com/blanketops/environments/pkg/apis/build/application"
	"github.com/blanketops/environments/pkg/apis/build/domain"
	"github.com/go-logr/logr"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler observes Shipwright BuildRun resources and feeds their
// terminal Succeeded condition back to the owning Build CR's contract
// status and conditions.
type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *events.EventRecorder
}

// Reconcile exits immediately for non-terminal BuildRuns, resolves the
// owning Build via the build.blanketops.dev/name label, and writes the
// outcome to its status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "buildrun-observer", "buildRun", req.String())
	log.Info("reconcile start")

	var br shipwrightv1alpha1.BuildRun
	if err := r.Get(ctx, req.NamespacedName, &br); err != nil {
		log.Info("buildrun not found, ignoring")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cond := br.Status.GetCondition("Succeeded")
	if cond == nil || cond.Status == corev1.ConditionUnknown {
		log.Info("skipping: buildrun not terminal yet")
		return ctrl.Result{}, nil
	}

	success := cond.Status == corev1.ConditionTrue
	log = log.WithValues("succeeded", success, "reason", cond.Reason)

	buildName := br.Labels["build.blanketops.dev/name"]
	if buildName == "" {
		log.Info("skipping: buildrun has no owning build label")
		return ctrl.Result{}, nil
	}

	var build buildv1.Build
	if err := r.Get(ctx, client.ObjectKey{Namespace: br.Namespace, Name: buildName}, &build); err != nil {
		log.Error(err, "failed to fetch owning build")
		return ctrl.Result{}, err
	}

	log = log.WithValues("build", build.Name, "namespace", build.Namespace)
	log.Info("buildrun completed")

	if r.Recorder != nil {
		if success {
			r.Recorder.Normal(&build, "BuildSucceeded", "BuildRun %s completed successfully", br.Name)
		} else {
			r.Recorder.Warn(&build, "BuildFailed", "BuildRun %s failed: %s", br.Name, cond.Message)
		}
	}

	conditions := r.buildContractAndConditions(&build, &br, success, cond.Message, log)

	return ctrl.Result{}, r.Status.Write(ctx, &build, conditions...)
}

func (r *Reconciler) buildContractAndConditions(
	build *buildv1.Build,
	br *shipwrightv1alpha1.BuildRun,
	success bool,
	message string,
	log logr.Logger,
) []metav1.Condition {
	now := metav1.NewTime(time.Now())

	var currentStatus domain.BuildStatus
	if len(build.Status.Contract.Raw) > 0 {
		_ = json.Unmarshal(build.Status.Contract.Raw, &currentStatus)
	}

	buildHash := br.Labels["build-hash"]

	contractStatus := domain.BuildStatus{
		Success:      success,
		Message:      message,
		ExecutionRef: br.Name,
		BuildHash:    buildHash,
		Triggered:    true,
	}

	raw, err := json.Marshal(contractStatus)
	if err != nil {
		log.Error(err, "failed to marshal contract status")
	} else {
		build.Status.Contract = runtime.RawExtension{Raw: raw}
	}

	var condition metav1.Condition
	if success {
		condition = metav1.Condition{
			Type:               "BuildSuccess",
			Status:             metav1.ConditionTrue,
			Reason:             "BuildSucceeded",
			Message:            message,
			LastTransitionTime: now,
		}
	} else {
		condition = metav1.Condition{
			Type:               "BuildFailed",
			Status:             metav1.ConditionFalse,
			Reason:             "BuildFailed",
			Message:            message,
			LastTransitionTime: now,
		}
	}

	return []metav1.Condition{condition}
}

// SetupWithManager registers the BuildRun observer with the controller
// manager, watching Shipwright BuildRun resources rather than Build CRs —
// the BuildRun is what signals whether the build actually succeeded.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = events.NewEventRecorder(mgr.GetEventRecorder("buildrun-observer"))
	return ctrl.NewControllerManagedBy(mgr).
		For(&shipwrightv1alpha1.BuildRun{}).
		Complete(r)
}
