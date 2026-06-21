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

package buildrun

import (
	"context"

	buildv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/domain"
	buildresolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	shipwrightv1beta1 "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	corev1 "k8s.io/api/core/v1"
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

	var br shipwrightv1beta1.BuildRun
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

	_, err := buildresolution.ResolveBuild(&build)
	if err != nil {
		log.Error(err, "failed to resolve build contract")
		return ctrl.Result{}, err
	}

	buildHash := br.Labels["build-hash"]
	log = log.WithValues("buildHash", buildHash)
	log.Info("buildrun completed")

	if r.Recorder != nil {
		if success {
			r.Recorder.Normal(&build, "BuildSucceeded", "BuildRun %s completed successfully", br.Name)
		} else {
			r.Recorder.Warn(&build, "BuildFailed", "BuildRun %s failed: %s", br.Name, cond.Message)
		}
	}

	log.Info("finalizing build status")

	result := domain.BuildResult{
		Success:      success,
		Message:      cond.Message,
		ExecutionRef: br.Name,
		BuildHash:    buildHash,
	}

	if br.Status.Output != nil && br.Status.Output.Digest != "" {
		result.ArtifactRef = br.Status.Output.Digest
	}

	return ctrl.Result{}, r.Status.Write(ctx, &build, result, nil)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("buildrun-observer"))
	return ctrl.NewControllerManagedBy(mgr).
		For(&shipwrightv1beta1.BuildRun{}).
		Complete(r)
}
