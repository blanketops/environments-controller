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

package buildrun

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-logr/logr"
	buildv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/domain"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *core.EventRecorder
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "buildrun-observer", "buildRun", req.NamespacedName.String())
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

	if br.Status.Output != nil && br.Status.Output.Digest != "" {
		//contractStatus.ArtifactRef = br.Status.Output.Digest
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

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("buildrun-observer"))
	return ctrl.NewControllerManagedBy(mgr).
		For(&shipwrightv1alpha1.BuildRun{}).
		Complete(r)
}
