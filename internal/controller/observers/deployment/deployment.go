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
Package deployment implements the Deployment observer.

The Deployment observer bridges the Flux CD delivery layer and the BlanketOps
deployment model. It watches Flux Kustomization resources — the objects that
FluxCD creates and drives to reconcile manifests from the manifests repository
onto the target cluster — and feeds their terminal reconciliation outcomes back
to the owning Deployment CR.

Responsibility boundary:
  - Watch Flux Kustomization resources for Ready condition transitions.
  - Resolve the owning Deployment CR via the deployment.blanketops.dev/name label.
  - Map the Flux Ready condition to a domain DeploymentPhase.
  - Emit Kubernetes events on the owning Deployment CR.
  - Delegate final status persistence to the application StatusWriter.
  - MUST NOT mutate domain state beyond status writes.

Architecture note:
The Deployment CR owns the Flux Kustomization as a child resource — it is
created and managed by the Deployment controller (domain layer), not by this
observer. This observer exists solely to reflect the Kustomization's
reconciliation outcome back up to the parent Deployment CR, completing the
GitOps feedback loop: declare → apply → observe → report.
*/
package deployment

import (
	"context"
	"time"

	environmentv1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	fluxkustomize "github.com/fluxcd/kustomize-controller/api/v1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/deployment/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/deployment/domain"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler observes Flux Kustomization resources on behalf of the Deployment
// domain. It translates Flux reconciliation outcomes into Deployment status
// updates via the application StatusWriter.
type Reconciler struct {
	client.Client
	// Status persists the final Deployment outcome once a terminal Kustomization
	// state is observed.
	Status *application.StatusWriter
	// Recorder emits Kubernetes events on the owning Deployment resource.
	Recorder *core.EventRecorder
}

// Reconcile is invoked by controller-runtime for every Flux Kustomization event.
// It exits immediately for non-terminal states, resolves the owning Deployment,
// maps the Flux condition to a domain phase, and writes the final status.
func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	// ------------------------------------------------
	// Fetch Kustomization.
	// ------------------------------------------------
	var ks fluxkustomize.Kustomization
	if err := r.Get(ctx, req.NamespacedName, &ks); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// Extract the Flux Ready condition.
	//
	// Flux uses metav1.Condition (not corev1.ConditionStatus) so we scan
	// the conditions slice directly rather than using a helper. The Ready
	// condition is the authoritative signal for Kustomization health.
	// ------------------------------------------------
	var readyCond *metav1.Condition
	for i := range ks.Status.Conditions {
		if ks.Status.Conditions[i].Type == "Ready" {
			readyCond = &ks.Status.Conditions[i]
			break
		}
	}

	// ------------------------------------------------
	// Only act on terminal Kustomizations.
	//
	// ConditionUnknown means Flux is still reconciling. We have no outcome
	// to report until the condition resolves to True or False.
	// ------------------------------------------------
	if readyCond == nil || readyCond.Status == metav1.ConditionUnknown {
		return ctrl.Result{}, nil
	}

	// ------------------------------------------------
	// Resolve the owning Deployment CR via label.
	//
	// The Deployment domain stamps this label on every Kustomization it
	// creates. Kustomizations without the label are not platform-owned
	// and are silently ignored.
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
	// Resolve the authoritative Deployment contract.
	//
	// Runtime and other policy fields must come from the resolved contract,
	// not from cached or stale CR fields.
	// ------------------------------------------------
	resolved, err := deploymentResolution.ResolveDeployment(&deployment)
	if err != nil {
		return ctrl.Result{}, err
	}

	// ------------------------------------------------
	// Map Flux Ready condition → domain DeploymentPhase.
	//
	// Flux's ConditionTrue/False maps cleanly to Ready/Failed. The default
	// case guards against any future Flux condition values we haven't
	// accounted for, landing them in Reconciling rather than silently
	// dropping them.
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
	// Emit terminal events on the owning Deployment.
	//
	// Events are best-effort — a nil Recorder is safe and results in a no-op.
	// Reconciling phase produces no event; we only emit on terminal outcomes.
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
	// Write final Deployment status.
	//
	// Single authoritative write per observation cycle. The StatusWriter
	// owns the patch strategy and condition merging.
	// ------------------------------------------------
	return ctrl.Result{}, r.Status.WriteDeploymentResult(
		ctx,
		&deployment,
		result,
		nil,
	)
}

// SetupWithManager registers the Deployment observer with the controller manager.
//
// The observer watches Flux Kustomization resources rather than Deployment CRs.
// This is intentional — the Kustomization is the object whose Ready condition
// signals whether GitOps delivery succeeded or failed. Watching the Deployment
// CR itself would give us no signal about what Flux actually did on the cluster.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("deployment-observer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&fluxkustomize.Kustomization{}).
		Complete(r)
}
