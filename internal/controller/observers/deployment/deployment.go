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
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package deployment

import (
	"context"
	"time"

	fluxkustomize "github.com/fluxcd/kustomize-controller/api/v1"
	environmentv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/deployment/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/deployment/domain"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler observes Flux Kustomizations and updates Deployment status
// based on terminal reconciliation outcomes.
type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *core.EventRecorder
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	var ks fluxkustomize.Kustomization
	if err := r.Get(ctx, req.NamespacedName, &ks); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// Extract Ready condition (Flux is raw)
	// ------------------------------------------------
	var readyCond *metav1.Condition
	for i := range ks.Status.Conditions {
		if ks.Status.Conditions[i].Type == "Ready" {
			readyCond = &ks.Status.Conditions[i]
			break
		}
	}

	// Not terminal → ignore
	if readyCond == nil || readyCond.Status == metav1.ConditionUnknown {
		return ctrl.Result{}, nil
	}

	// ------------------------------------------------
	// Resolve owning Deployment CR
	// ------------------------------------------------
	deploymentName := ks.Labels["deployment.blanketops.dev/name"]
	if deploymentName == "" {
		return ctrl.Result{}, nil
	}

	var deployment environmentv1.Deployment
	if err := r.Get(
		ctx,
		types.NamespacedName{
			Name:      deploymentName,
			Namespace: ks.Namespace,
		},
		&deployment,
	); err != nil {
		return ctrl.Result{}, err
	}

	// ------------------------------------------------
	// Resolve Deployment (AUTHORITATIVE)
	// ------------------------------------------------
	resolved, err := deploymentResolution.ResolveDeployment(&deployment)
	if err != nil {
		return ctrl.Result{}, err
	}

	// ------------------------------------------------
	// Map Flux condition → domain result
	// ------------------------------------------------
	var phase domain.DeploymentPhase

	switch readyCond.Status {
	case metav1.ConditionTrue:
		phase = domain.DeploymentPhase("Ready")

	case metav1.ConditionFalse:
		phase = domain.DeploymentPhase("Failed")

	default:
		phase = domain.DeploymentPhase("Reconciling")
	}

	result := &domain.DeploymentResult{
		Phase:          phase,
		Message:        readyCond.Message,
		Runtime:        domain.Runtime(resolved.Spec.Runtime),
		LastUpdateTime: time.Now(),
	}

	// ------------------------------------------------
	// Emit events (terminal only)
	// ------------------------------------------------
	if r.Recorder != nil {
		switch phase {

		case domain.DeploymentPhase("Ready"):
			r.Recorder.Normal(
				&deployment,
				"DeploymentSucceeded",
				"Kustomization %s applied successfully",
				ks.Name,
			)

		case domain.DeploymentPhase("Failed"):
			r.Recorder.Warn(
				&deployment,
				"DeploymentFailed",
				"Kustomization %s failed: %s",
				ks.Name,
				readyCond.Message,
			)
		}
	}

	// ------------------------------------------------
	// Write status (single authoritative write)
	// ------------------------------------------------
	return ctrl.Result{}, r.Status.WriteDeploymentResult(
		ctx,
		&deployment,
		result,
		nil,
	)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("deployment-observer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&fluxkustomize.Kustomization{}).
		Complete(r)
}
