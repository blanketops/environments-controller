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

package environments

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/conditions"
	"github.com/blanketops/environments/core/engine"
	"github.com/blanketops/environments/core/registry"
	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const (
	condBuildStart   = "BuildStart"
	condBuildSuccess = "BuildSuccess"
	observedContract = `{"ExecutionRef":"run-2","Image":"ghcr.io/acme/app:2@sha256:new","Success":true}`
)

// observerRaceDomain stands in for the Build domain. While the reconciler
// is inside Handle it writes status to the cluster the way the buildrun
// observer does, then sets a condition on the object the reconciler holds.
type observerRaceDomain struct {
	c client.Client
}

func (d *observerRaceDomain) GVK() schema.GroupVersionKind {
	return environmentsv1alpha1.GroupVersion.WithKind("Build")
}

func (d *observerRaceDomain) Handle(ctx context.Context, cmd command.Command) error {
	var latest environmentsv1alpha1.Build
	if err := d.c.Get(ctx, client.ObjectKeyFromObject(cmd.Obj), &latest); err != nil {
		return err
	}
	latest.Status.Contract = runtime.RawExtension{Raw: []byte(observedContract)}
	apimeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type: condBuildSuccess, Status: metav1.ConditionTrue, Reason: "BuildSucceeded", Message: "run-2 succeeded",
	})
	if err := d.c.Status().Update(ctx, &latest); err != nil {
		return err
	}

	build := cmd.Obj.(*environmentsv1alpha1.Build)
	conditions.SetCondition(&build.Status.Conditions, condBuildStart, conditions.ConditionTrue, "BuildRunStarted", "Build run has started")
	return nil
}

func (d *observerRaceDomain) CanCreate(client.Object) bool      { return true }
func (d *observerRaceDomain) CanUpdate(_, _ client.Object) bool { return true }
func (d *observerRaceDomain) CanDelete(client.Object) bool      { return true }

func TestBuildReconciler_KeepsStatusWrittenDuringReconcile(t *testing.T) {
	build := &environmentsv1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "app-build",
			Namespace:  "default",
			Finalizers: []string{buildFinalizer},
		},
	}
	// What the reconciler reads at the start: the outcome of an older run.
	build.Status.Contract = runtime.RawExtension{Raw: []byte(`{"Success":false,"ExecutionRef":"run-1"}`)}
	build.Status.Conditions = []metav1.Condition{{
		Type: condBuildSuccess, Status: metav1.ConditionFalse, Reason: "BuildFailed", Message: "run-1 failed",
		LastTransitionTime: metav1.Now(),
	}}

	c := testsupport.NewFakeClient(build)
	reg := registry.NewRegistry()
	reg.RegisterDomain(environmentsv1alpha1.GroupVersion.WithKind("Build"), &observerRaceDomain{c: c})
	r := &BuildReconciler{
		Client:   c,
		Scheme:   testsupport.NewScheme(),
		Log:      logr.Discard(),
		Recorder: testsupport.NoopRawRecorder(),
		Runtime: &runtimeinfra.Runtime{
			Cache:    &corecache.Cache{External: corecache.NoopExternalCache{}},
			Events:   testsupport.NoopRecorder(),
			Registry: reg,
			Engine:   engine.NewEngine(reg, logr.Discard()),
			Log:      logr.Discard(),
		},
	}

	key := client.ObjectKeyFromObject(build)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got environmentsv1alpha1.Build
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get build: %v", err)
	}
	if string(got.Status.Contract.Raw) != observedContract {
		t.Errorf("contract = %s, want the one written during reconcile %s", got.Status.Contract.Raw, observedContract)
	}
	success := apimeta.FindStatusCondition(got.Status.Conditions, condBuildSuccess)
	if success == nil || success.Status != metav1.ConditionTrue || success.Message != "run-2 succeeded" {
		t.Errorf("%s = %+v, want the condition written during reconcile", condBuildSuccess, success)
	}
	if start := apimeta.FindStatusCondition(got.Status.Conditions, condBuildStart); start == nil || start.Status != metav1.ConditionTrue {
		t.Errorf("%s = %+v, want the condition the domain set", condBuildStart, start)
	}
}

func TestChangedConditions(t *testing.T) {
	now := metav1.Now()
	kept := metav1.Condition{Type: "Kept", Status: metav1.ConditionTrue, Reason: "R", LastTransitionTime: now}
	oldVal := metav1.Condition{Type: "Flipped", Status: metav1.ConditionFalse, Reason: "R", LastTransitionTime: now}
	newVal := metav1.Condition{Type: "Flipped", Status: metav1.ConditionTrue, Reason: "R", LastTransitionTime: now}
	added := metav1.Condition{Type: "Added", Status: metav1.ConditionTrue, Reason: "R", LastTransitionTime: now}

	got := changedConditions([]metav1.Condition{kept, oldVal}, []metav1.Condition{kept, newVal, added})
	if len(got) != 2 || got[0] != newVal || got[1] != added {
		t.Errorf("changedConditions() = %+v, want the flipped and the added condition", got)
	}
	if got := changedConditions(nil, nil); len(got) != 0 {
		t.Errorf("changedConditions(nil, nil) = %+v, want none", got)
	}
}
