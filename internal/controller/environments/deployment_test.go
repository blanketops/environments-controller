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

// package.go reconciles the Package CR: routes create/update through the
// core CQRS engine and, on setup, wires the Package domain's mediator,

package environments

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/engine"
	"github.com/blanketops/environments/core/registry"
	"github.com/blanketops/environments/pkg/apis/deployment/api"
	"github.com/blanketops/environments/pkg/apis/deployment/application"
	"github.com/blanketops/environments/pkg/apis/deployment/reconcile"
	"github.com/blanketops/environments/pkg/apis/deployment/strategy"
	deploymentintent "github.com/blanketops/environments/pkg/intent/deployment"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	deploydomain "github.com/blanketops/environments-controller/internal/domains/deployment"
	deploymentmediator "github.com/blanketops/environments-controller/internal/mediators/deployment"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const (
	deplNamespace = "delivery"
	deplName      = "site"
	deplUnit      = "web"
	deplEnv       = "site-env"
	deplEnvType   = "development"
)

var deplKey = client.ObjectKey{Namespace: deplNamespace, Name: deplName}

func deplLabels() map[string]string {
	return map[string]string{
		"environments.blanketops.dev/name": deplEnv,
		"environments.blanketops.dev/type": deplEnvType,
	}
}

func newDeploymentFixtures() []client.Object {
	env := &environmentsv1alpha1.Environment{ObjectMeta: metav1.ObjectMeta{Name: deplEnv, Namespace: deplNamespace, Labels: deplLabels()}}
	env.Spec.Contract = testsupport.RawContract(map[string]any{
		"applicationName": deplName, "branch": "main", "gitOwner": "example-org", "environmentType": deplEnvType, "version": "v1",
	})
	su := &environmentsv1alpha1.ServiceUnit{ObjectMeta: metav1.ObjectMeta{Name: deplUnit, Namespace: deplNamespace, Labels: deplLabels()}}
	su.Spec.Contract = testsupport.RawContract(map[string]any{"type": "static", "image": "ghcr.io/example-org/web:v1", "containerPort": 8080, "size": 1})
	depl := &environmentsv1alpha1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deplName, Namespace: deplNamespace, Labels: deplLabels()}}
	depl.Spec.Contract = testsupport.RawContract(map[string]any{
		"serviceUnits": []any{deplUnit}, "runtime": "kubernetes", "strategy": "Rolling",
	})
	return []client.Object{env, su, depl}
}

// newDeploymentReconciler wires the reconciler the way SetupWithManager does,
// with the imperative runtime, against a fake client.
func newDeploymentReconciler(c client.Client) *DeploymentReconciler {
	scheme := testsupport.NewScheme()
	log := logr.Discard()
	raw := testsupport.NoopRawRecorder()

	service := application.NewDeploymentService(
		deploymentintent.NewIntentBuilder(),
		application.NewStatusWriter(c, log),
		reconcile.NewReconciliationExecutor(
			strategy.NewRuntimeProvider(c, scheme, log, raw),
			api.NewKustomizeStrategyProvider(c, scheme, log),
			log,
		),
		log,
	)
	reg := registry.NewRegistry()
	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	reg.RegisterDomain(environmentsv1alpha1.GroupVersion.WithKind("Deployment"),
		deploydomain.New(deploymentmediator.New(c, scheme, log, raw), service, cache, c, testsupport.NoopRecorder(), log))

	return &DeploymentReconciler{
		Client: c, Scheme: scheme, Log: log, Recorder: raw,
		Runtime: &runtimeinfra.Runtime{
			Cache: cache, Events: testsupport.NoopRecorder(), Registry: reg,
			Engine: engine.NewEngine(reg, log), Log: log,
		},
	}
}

func reconcileDeployment(t *testing.T, r *DeploymentReconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: deplKey})
	return err
}

// Deleting a Deployment removes the workload it rolled out, and only then
// releases its finalizer. The reconciler used to send an update on delete,
// which re-applied the workload and then let the Deployment go, leaving the
// workload running with nothing to own it.
func TestDeploymentReconciler_DeleteTearsDownTheWorkload(t *testing.T) {
	c := testsupport.NewFakeClient(newDeploymentFixtures()...)
	r := newDeploymentReconciler(c)
	ctx := context.Background()
	workload := client.ObjectKey{Namespace: deplNamespace, Name: deplUnit}

	_ = reconcileDeployment(t, r) // finalizer
	if err := reconcileDeployment(t, r); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := c.Get(ctx, workload, &appsv1.Deployment{}); err != nil {
		t.Fatalf("workload Deployment after reconcile: %v", err)
	}
	if err := c.Get(ctx, workload, &corev1.Service{}); err != nil {
		t.Fatalf("workload Service after reconcile: %v", err)
	}

	depl := &environmentsv1alpha1.Deployment{}
	if err := c.Get(ctx, deplKey, depl); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if !controllerutil.ContainsFinalizer(depl, deploymentFinalizer) {
		t.Fatal("the Deployment must carry its finalizer before it is deleted")
	}
	if err := c.Delete(ctx, depl); err != nil {
		t.Fatalf("delete deployment: %v", err)
	}

	if err := reconcileDeployment(t, r); err != nil {
		t.Fatalf("delete Reconcile: %v", err)
	}

	if err := c.Get(ctx, workload, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Errorf("workload Deployment after delete: err = %v, want not found", err)
	}
	if err := c.Get(ctx, workload, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Errorf("workload Service after delete: err = %v, want not found", err)
	}
	if err := c.Get(ctx, deplKey, depl); !apierrors.IsNotFound(err) {
		t.Errorf("Deployment after teardown: err = %v, want not found", err)
	}

	// A reconcile after the object is gone is a no-op.
	if err := reconcileDeployment(t, r); err != nil {
		t.Errorf("Reconcile after deletion: %v", err)
	}
}
