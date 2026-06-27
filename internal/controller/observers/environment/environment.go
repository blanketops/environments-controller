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
Package environment (environment-observer) is the status rollup reconciler
for Environment CRs.

It fans in from all composed CR types — Build, GitRepository, Deployment,
Route, Package, and ServiceUnit — and aggregates their readiness into a
single EnvironmentStatus: phase + per-resource conditions.

Sole writer to EnvironmentStatus. Never touches composed CR specs.
CQRS boundary: this reconciler has no command authority.

All composed CR refs are resolved through ResolveEnvironment — never via
direct field access on env.Spec. The Environment CR stores its spec as
raw JSON in Spec.Contract.

Phase computation:
  - All conditions True → ENVIRONMENT_PHASE_READY
  - Any condition False → ENVIRONMENT_PHASE_PENDING
*/
package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	environmentsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	networksv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/networks/v1alpha1"
	sourcesv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/sources/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	environmentresolution "github.com/ntlaletsi70/blanketops-environments/resolution/environment"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type Reconciler struct {
	client.Client
	Recorder *core.EventRecorder
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"controller", "environment-observer",
		"environment", req.NamespacedName.String(),
	)
	log.Info("reconcile start")

	var env environmentsv1alpha1.Environment
	if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ── Resolve environment contract ──────────────────────────────────────────
	// All CR refs live in Spec.Contract.Raw — never access env.Spec fields directly.
	resolved, err := environmentresolution.ResolveEnvironment(&env)
	if err != nil {
		log.Error(err, "failed to resolve environment contract")
		return ctrl.Result{}, err
	}

	ns := env.Namespace
	now := metav1.NewTime(time.Now())
	conditions := make([]metav1.Condition, 0)

	// ── Build ──────────────────────────────────────────────────────────────────
	if resolved.Spec.Build != "" {
		var build environmentsv1alpha1.Build
		ready, msg := false, ""
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: resolved.Spec.Build}, &build); err != nil {
			msg = fmt.Sprintf("Build %s not found", resolved.Spec.Build)
		} else {
			ready, msg = checkReady(build.Status.Conditions)
		}
		conditions = append(conditions, makeCondition("BuildReady", ready, "Build", msg, now))
	}

	// ── GitRepository ──────────────────────────────────────────────────────────
	if resolved.Spec.GitRepository != "" {
		var repo sourcesv1alpha1.GitRepository
		ready, msg := false, ""
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: resolved.Spec.GitRepository}, &repo); err != nil {
			msg = fmt.Sprintf("GitRepository %s not found", resolved.Spec.GitRepository)
		} else {
			ready, msg = checkReady(repo.Status.Conditions)
		}
		conditions = append(conditions, makeCondition("GitRepositoryReady", ready, "GitRepository", msg, now))
	}

	// ── Deployment ─────────────────────────────────────────────────────────────
	if resolved.Spec.Deployment != "" {
		var deployment environmentsv1alpha1.Deployment
		ready, msg := false, ""
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: resolved.Spec.Deployment}, &deployment); err != nil {
			msg = fmt.Sprintf("Deployment %s not found", resolved.Spec.Deployment)
		} else {
			ready, msg = checkReady(deployment.Status.Conditions)
		}
		conditions = append(conditions, makeCondition("DeploymentReady", ready, "Deployment", msg, now))
	}

	// ── Route (optional) ──────────────────────────────────────────────────────
	if resolved.Spec.Route != "" {
		var route networksv1alpha1.Route
		ready, msg := false, ""
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: resolved.Spec.Route}, &route); err != nil {
			msg = fmt.Sprintf("Route %s not found", resolved.Spec.Route)
		} else {
			ready, msg = checkReady(route.Status.Conditions)
		}
		conditions = append(conditions, makeCondition("RouteReady", ready, "Route", msg, now))
	}

	// ── Package (optional) ────────────────────────────────────────────────────
	if resolved.Spec.Package != "" {
		var pkg environmentsv1alpha1.Package
		ready, msg := false, ""
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: resolved.Spec.Package}, &pkg); err != nil {
			msg = fmt.Sprintf("Package %s not found", resolved.Spec.Package)
		} else {
			ready, msg = checkReady(pkg.Status.Conditions)
		}
		conditions = append(conditions, makeCondition("PackageReady", ready, "Package", msg, now))
	}

	// ── ServiceUnits ──────────────────────────────────────────────────────────
	for _, ref := range resolved.Spec.ServiceUnits {
		if ref == "" {
			continue
		}
		var su environmentsv1alpha1.ServiceUnit
		ready, msg := false, ""
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref}, &su); err != nil {
			msg = fmt.Sprintf("ServiceUnit %s not found", ref)
		} else {
			ready, msg = checkReady(su.Status.Conditions)
		}
		conditions = append(conditions, makeCondition(
			fmt.Sprintf("ServiceUnit.%s.Ready", ref),
			ready, ref, msg, now,
		))
	}

	// ── Patch status ──────────────────────────────────────────────────────────
	// ── Patch status ──────────────────────────────────────────────────────────
	phase := aggregatePhase(conditions)

	// Phase is a domain concern — written to Status.Contract.Raw.
	// Conditions are Kubernetes-native — written to Status.Conditions.
	contractStatus := map[string]any{
		"phase": phase,
	}
	raw, err := json.Marshal(contractStatus)
	if err != nil {
		log.Error(err, "failed to marshal environment status contract")
		return ctrl.Result{}, err
	}

	original := env.DeepCopy()
	env.Status.Contract = runtime.RawExtension{Raw: raw}
	env.Status.Conditions = conditions

	if err := r.Status().Patch(ctx, &env, client.MergeFrom(original)); err != nil {
		log.Error(err, "failed to patch environment status")
		return ctrl.Result{}, err
	}

	log.Info("reconcile complete", "phase", phase, "conditions", len(conditions))
	return ctrl.Result{}, nil
}

// checkReady returns true when a Ready=True condition exists in the slice.
func checkReady(conditions []metav1.Condition) (bool, string) {
	for _, c := range conditions {
		if c.Type == "Ready" {
			return c.Status == metav1.ConditionTrue, c.Message
		}
	}
	return false, "no Ready condition"
}

// makeCondition builds a standard environment component condition.
func makeCondition(condType string, ready bool, reason, message string, now metav1.Time) metav1.Condition {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	}
}

// aggregatePhase computes the Environment phase from composed CR conditions.
func aggregatePhase(conditions []metav1.Condition) string {
	if len(conditions) == 0 {
		return "ENVIRONMENT_PHASE_PENDING"
	}
	for _, c := range conditions {
		if c.Status != metav1.ConditionTrue {
			return "ENVIRONMENT_PHASE_PENDING"
		}
	}
	return "ENVIRONMENT_PHASE_READY"
}

// mapToEnvironment fans changes on composed CRs back to the owning Environment.
func (r *Reconciler) mapToEnvironment(ctx context.Context, obj client.Object) []reconcile.Request {
	appName := obj.GetLabels()["environments.blanketops.dev/name"]
	if appName == "" {
		return nil
	}

	var envList environmentsv1alpha1.EnvironmentList
	if err := r.List(ctx, &envList,
		client.MatchingLabels{"environments.blanketops.dev/name": appName},
	); err != nil {
		return nil
	}

	reqs := make([]reconcile.Request, 0, len(envList.Items))
	for _, env := range envList.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&env),
		})
	}
	return reqs
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("environment-observer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&environmentsv1alpha1.Environment{}).
		Watches(&environmentsv1alpha1.Build{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Watches(&sourcesv1alpha1.GitRepository{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Watches(&environmentsv1alpha1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		//Watches(&networksv1alpha1.Route{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Watches(&environmentsv1alpha1.Package{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Watches(&environmentsv1alpha1.ServiceUnit{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Complete(r)
}
