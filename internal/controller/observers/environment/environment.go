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

It also auto-discovers composed CRs by label and patches their refs back
into spec.contract so the Environment stays current without requiring the
operator to declare them upfront.

Sole writer to EnvironmentStatus. Spec contract ref patching is additive only.
CQRS boundary: this reconciler never touches composed CR specs.

Readiness is per-domain:
  - Build       → BuildSuccess=True (set by buildrun-observer)
  - GitRepository → Ready=True
  - Deployment  → Ready=True
  - Route       → Ready=True
  - Package     → Ready=True
  - ServiceUnit → Ready=True

Phase computation:
  - All conditions True  → ENVIRONMENT_PHASE_READY
  - Any condition False  → ENVIRONMENT_PHASE_PENDING
  - No conditions        → ENVIRONMENT_PHASE_PENDING
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// contractKeyName is the map key used when constructing the environment
	// contract payload. Extracted as a constant — 6 occurrences in this file.
	contractKeyName = "name"
)

type Reconciler struct {
	client.Client
	Recorder *core.EventRecorder
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"controller", "environment-observer",
		"environment", req.String(),
	)
	log.Info("reconcile start")

	var env environmentsv1alpha1.Environment
	if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	ns := env.Namespace
	appName := env.Name
	now := metav1.NewTime(time.Now())
	conditions := make([]metav1.Condition, 0)

	labelSelector := client.MatchingLabels{"environments.blanketops.dev/name": appName}

	// ── Decode current spec contract ──────────────────────────────────────────
	// We patch refs additively — never remove what's already there.
	var contractRaw map[string]any
	if len(env.Spec.Contract.Raw) > 0 {
		if err := json.Unmarshal(env.Spec.Contract.Raw, &contractRaw); err != nil {
			log.Error(err, "failed to decode spec contract")
			return ctrl.Result{}, err
		}
	} else {
		contractRaw = map[string]any{}
	}
	contractChanged := false

	// ── Build ──────────────────────────────────────────────────────────────────
	var builds environmentsv1alpha1.BuildList
	if err := r.List(ctx, &builds, client.InNamespace(ns), labelSelector); err == nil {
		for _, build := range builds.Items {
			// Patch ref into spec.contract if not already there.
			if _, ok := contractRaw["build"]; !ok {
				contractRaw["build"] = map[string]any{contractKeyName: build.Name}
				contractChanged = true
				log.Info("patching build ref into spec.contract", "build", build.Name)
			}
			ready, msg := checkBuildReady(build.Status.Conditions)
			conditions = append(conditions, makeCondition(
				fmt.Sprintf("Build.%s.Ready", build.Name), ready, "Build", msg, now,
			))
		}
	}

	// ── GitRepository ──────────────────────────────────────────────────────────
	var repos sourcesv1alpha1.GitRepositoryList
	if err := r.List(ctx, &repos, client.InNamespace(ns), labelSelector); err == nil {
		for _, repo := range repos.Items {
			if _, ok := contractRaw["gitRepository"]; !ok {
				contractRaw["gitRepository"] = map[string]any{contractKeyName: repo.Name}
				contractChanged = true
				log.Info("patching gitRepository ref into spec.contract", "gitRepository", repo.Name)
			}
			ready, msg := checkReady(repo.Status.Conditions)
			conditions = append(conditions, makeCondition(
				fmt.Sprintf("GitRepository.%s.Ready", repo.Name), ready, "GitRepository", msg, now,
			))
		}
	}

	// ── Deployment ─────────────────────────────────────────────────────────────
	var deployments environmentsv1alpha1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(ns), labelSelector); err == nil {
		for _, deployment := range deployments.Items {
			if _, ok := contractRaw["deployment"]; !ok {
				contractRaw["deployment"] = map[string]any{contractKeyName: deployment.Name}
				contractChanged = true
				log.Info("patching deployment ref into spec.contract", "deployment", deployment.Name)
			}
			ready, msg := checkReady(deployment.Status.Conditions)
			conditions = append(conditions, makeCondition(
				fmt.Sprintf("Deployment.%s.Ready", deployment.Name), ready, "Deployment", msg, now,
			))
		}
	}

	// ── Route ─────────────────────────────────────────────────────────────────
	var routes networksv1alpha1.RouteList
	if err := r.List(ctx, &routes, client.InNamespace(ns), labelSelector); err == nil {
		for _, route := range routes.Items {
			if _, ok := contractRaw["route"]; !ok {
				contractRaw["route"] = map[string]any{contractKeyName: route.Name}
				contractChanged = true
				log.Info("patching route ref into spec.contract", "route", route.Name)
			}
			ready, msg := checkReady(route.Status.Conditions)
			conditions = append(conditions, makeCondition(
				fmt.Sprintf("Route.%s.Ready", route.Name), ready, "Route", msg, now,
			))
		}
	}

	// ── Package ───────────────────────────────────────────────────────────────
	var packages environmentsv1alpha1.PackageList
	if err := r.List(ctx, &packages, client.InNamespace(ns), labelSelector); err == nil {
		for _, pkg := range packages.Items {
			if _, ok := contractRaw["package"]; !ok {
				contractRaw["package"] = map[string]any{contractKeyName: pkg.Name}
				contractChanged = true
				log.Info("patching package ref into spec.contract", "package", pkg.Name)
			}
			ready, msg := checkReady(pkg.Status.Conditions)
			conditions = append(conditions, makeCondition(
				fmt.Sprintf("Package.%s.Ready", pkg.Name), ready, "Package", msg, now,
			))
		}
	}

	// ── ServiceUnits ──────────────────────────────────────────────────────────
	var sus environmentsv1alpha1.ServiceUnitList
	if err := r.List(ctx, &sus, client.InNamespace(ns), labelSelector); err == nil {
		existing, _ := contractRaw["serviceUnits"].([]any)
		for _, su := range sus.Items {
			found := false
			for _, e := range existing {
				if m, ok := e.(map[string]any); ok {
					if m[contractKeyName] == su.Name {
						found = true
						break
					}
				}
			}
			if !found {
				existing = append(existing, map[string]any{contractKeyName: su.Name})
				contractRaw["serviceUnits"] = existing
				contractChanged = true
				log.Info("patching serviceUnit ref into spec.contract", "serviceUnit", su.Name)
			}
			ready, msg := checkReady(su.Status.Conditions)
			conditions = append(conditions, makeCondition(
				fmt.Sprintf("ServiceUnit.%s.Ready", su.Name), ready, su.Name, msg, now,
			))
		}
	}

	// ── Patch spec.contract if refs changed ───────────────────────────────────
	if contractChanged {
		newRaw, err := json.Marshal(contractRaw)
		if err != nil {
			log.Error(err, "failed to marshal updated spec contract")
			return ctrl.Result{}, err
		}
		original := env.DeepCopy()
		env.Spec.Contract = runtime.RawExtension{Raw: newRaw}
		if err := r.Patch(ctx, &env, client.MergeFrom(original)); err != nil {
			log.Error(err, "failed to patch spec contract")
			return ctrl.Result{}, err
		}
		log.Info("spec.contract patched with discovered refs")
		// Re-fetch after spec patch to get latest resourceVersion for status patch.
		if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	// ── Patch status ──────────────────────────────────────────────────────────
	phase := aggregatePhase(conditions)
	contractStatus := map[string]any{"phase": phase}
	rawStatus, err := json.Marshal(contractStatus)
	if err != nil {
		log.Error(err, "failed to marshal environment status contract")
		return ctrl.Result{}, err
	}

	original := env.DeepCopy()
	env.Status.Contract = runtime.RawExtension{Raw: rawStatus}
	env.Status.Conditions = conditions

	if err := r.Status().Patch(ctx, &env, client.MergeFrom(original)); err != nil {
		log.Error(err, "failed to patch environment status")
		return ctrl.Result{}, err
	}

	log.Info("reconcile complete", "phase", phase, "conditions", len(conditions))
	return ctrl.Result{}, nil
}

// checkBuildReady returns true when BuildSuccess=True is present.
// BuildSuccess is written by buildrun-observer on terminal success.
// BuildReady is dispatch intent only — not a terminal readiness signal.
func checkBuildReady(conditions []metav1.Condition) (bool, string) {
	for _, c := range conditions {
		if c.Type == "BuildSuccess" && c.Status == metav1.ConditionTrue {
			return true, c.Message
		}
	}
	return false, "no BuildSuccess condition"
}

// checkReady returns true when Ready=True is present.
// Used for all non-Build composed CR types.
func checkReady(conditions []metav1.Condition) (bool, string) {
	for _, c := range conditions {
		if c.Type == "Ready" && c.Status == metav1.ConditionTrue {
			return true, c.Message
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
		// Watches(&networksv1alpha1.Route{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Watches(&environmentsv1alpha1.Package{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Watches(&environmentsv1alpha1.ServiceUnit{}, handler.EnqueueRequestsFromMapFunc(r.mapToEnvironment)).
		Complete(r)
}
