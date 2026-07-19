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

package build

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	buildapi "github.com/blanketops/environments/pkg/apis/build/api"
	"github.com/blanketops/environments/pkg/apis/build/application"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildmediator "github.com/blanketops/environments-controller/internal/mediators/build"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const testAppName = "app-sample"

func newEnvironment() *environmentsv1alpha1.Environment {
	return &environmentsv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAppName,
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
				"environments.blanketops.dev/type": "dev",
			},
		},
		Spec: environmentsv1alpha1.EnvironmentSpec{
			Contract: testsupport.RawContract(map[string]any{
				"applicationName": testAppName,
				"branch":          "main",
				"gitOwner":        "blanketops",
				"environmentType": "dev",
				"version":         "v1",
			}),
		},
	}
}

func newBuildCR(contract map[string]any) *environmentsv1alpha1.Build {
	b := &environmentsv1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	if contract != nil {
		b.Spec.Contract = testsupport.RawContract(contract)
	}
	return b
}

func validBuildContract() map[string]any {
	return map[string]any{
		"image": "ghcr.io/blanketops/app:latest",
		"source": map[string]any{
			"url":         "https://github.com/blanketops/app.git",
			"cloneSecret": "app-git-ssh",
		},
		"strategy": map[string]any{
			"name": "kaniko",
			"kind": "ClusterBuildStrategy",
		},
	}
}

// newTestDomain wires a real BuildDomain against a fake client — the
// mediator and BuildService are the actual production types, not mocks,
// so Handle()'s orchestration is exercised end-to-end up to whatever the
// fake client can satisfy (no real Shipwright controller runs BuildRuns to
// completion, so success here means "dispatched correctly", not "the image
// built").
func newTestDomain(t *testing.T, objs ...client.Object) (*BuildDomain, client.Client) {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()
	rawRec := testsupport.NoopRawRecorder()

	med := buildmediator.New(c, testsupport.NewScheme(), log, rawRec)

	mapper := application.NewMapper()
	statusWriter := application.NewStatusWriter(c, log)
	backend := application.NewBackendSelector(
		buildapi.NewBuildahProvider(c, testsupport.NewScheme(), log, rawRec),
		buildapi.NewKanikoProvider(c, testsupport.NewScheme(), log, rawRec),
		buildapi.NewBuildpacksProvider(c, testsupport.NewScheme(), log, rawRec),
	)
	service := application.NewBuildService(mapper, statusWriter, backend)

	// Constructed directly rather than via corecache.NewCache — that
	// constructor dereferences a real ctrl.Manager (mgr.GetCache(),
	// mgr.GetFieldIndexer()), which the domain layer's own cache usage here
	// (ObjectCache.PublishResolved/Invalidate) never touches. Only External
	// is read, so a real manager would be pure unused ceremony in this test.
	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	d := New(med, service, cache, testsupport.NoopRecorder(), log)
	return d, c
}

func conditionStatus(conds []metav1.Condition, condType string) (metav1.ConditionStatus, bool) {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status, true
		}
	}
	return "", false
}

func TestBuildDomain_GVK(t *testing.T) {
	d := &BuildDomain{}
	gvk := d.GVK()
	if gvk.Kind != "Build" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "Build")
	}
	if gvk.Group != "environments.blanketops.dev" {
		t.Errorf("GVK().Group = %q, want %q", gvk.Group, "environments.blanketops.dev")
	}
}

func TestBuildDomain_CanCreate(t *testing.T) {
	d := &BuildDomain{}

	if !d.CanCreate(&environmentsv1alpha1.Build{}) {
		t.Error("CanCreate(*Build) = false, want true")
	}
	if d.CanCreate(&environmentsv1alpha1.Deployment{}) {
		t.Error("CanCreate(*Deployment) = true, want false")
	}
}

func TestBuildDomain_CanDelete(t *testing.T) {
	d := &BuildDomain{}

	if !d.CanDelete(&environmentsv1alpha1.Build{}) {
		t.Error("CanDelete(*Build) = false, want true")
	}
	if d.CanDelete(&environmentsv1alpha1.Deployment{}) {
		t.Error("CanDelete(*Deployment) = true, want false")
	}
}

func TestBuildDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newBuildCR(map[string]any{"image": "old", "source": map[string]any{"url": "x"}}),
			newObj: newBuildCR(map[string]any{"image": "new", "source": map[string]any{"url": "x"}}),
			want:   true,
		},
		{
			name: "spec unchanged",
			oldObj: &environmentsv1alpha1.Build{
				Spec: environmentsv1alpha1.BuildSpec{Contract: testsupport.RawContract(validBuildContract())},
			},
			newObj: &environmentsv1alpha1.Build{
				Spec: environmentsv1alpha1.BuildSpec{Contract: testsupport.RawContract(validBuildContract())},
			},
			want: false,
		},
		{
			name:   "wrong type old",
			oldObj: &environmentsv1alpha1.Deployment{},
			newObj: newBuildCR(validBuildContract()),
			want:   false,
		},
		{
			name:   "wrong type new",
			oldObj: newBuildCR(validBuildContract()),
			newObj: &environmentsv1alpha1.Deployment{},
			want:   false,
		},
	}

	d := &BuildDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildDomain_Handle_InvalidObject(t *testing.T) {
	d := &BuildDomain{}
	err := d.Handle(context.Background(), command.Command{Obj: &environmentsv1alpha1.Deployment{}})
	if err == nil {
		t.Fatal("Handle() with non-Build object = nil error, want error")
	}
}

func TestBuildDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	buildCR := newBuildCR(nil) // no contract -> resolution fails
	d, _ := newTestDomain(t, buildCR)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: buildCR})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}

	status, ok := conditionStatus(buildCR.Status.Conditions, "BuildResolveFailed")
	if !ok {
		t.Fatal("expected BuildResolveFailed condition to be set")
	}
	if status != metav1.ConditionFalse {
		t.Errorf("BuildResolveFailed condition status = %v, want %v", status, metav1.ConditionFalse)
	}
}

func TestBuildDomain_Handle_Create_MissingEnvironment(t *testing.T) {
	// Environment referenced by the label doesn't exist in the fake client —
	// the mediator's EnsurePrerequisites must fail at the query.Lookup gate.
	buildCR := newBuildCR(validBuildContract())
	d, _ := newTestDomain(t, buildCR)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: buildCR})
	if err == nil {
		t.Fatal("Handle() with no owning Environment = nil error, want error")
	}

	status, ok := conditionStatus(buildCR.Status.Conditions, "BuildPrerequisitesCreateFailed")
	if !ok {
		t.Fatal("expected BuildPrerequisitesCreateFailed condition to be set")
	}
	if status != metav1.ConditionFalse {
		t.Errorf("BuildPrerequisitesCreateFailed condition status = %v, want %v", status, metav1.ConditionFalse)
	}

	// Resolution itself must have succeeded before prerequisites ran.
	if status, ok := conditionStatus(buildCR.Status.Conditions, "BuildResolved"); !ok || status != metav1.ConditionTrue {
		t.Errorf("BuildResolved condition = (%v, found=%v), want (True, true)", status, ok)
	}
}

func TestBuildDomain_Handle_Create_PrerequisitesSucceed(t *testing.T) {
	env := newEnvironment()
	buildCR := newBuildCR(validBuildContract())
	d, _ := newTestDomain(t, env, buildCR)

	// Prerequisites (git SSH secret, registry secret, ServiceAccount) should
	// all provision successfully against the fake client once the owning
	// Environment exists. The build dispatch stage runs afterward and is
	// exercised regardless of whether it fully succeeds against the fake
	// client — what matters here is that Handle() reaches that stage rather
	// than failing earlier at the prerequisites gate.
	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: buildCR}); err != nil {
		t.Logf("Handle() returned (informational, not necessarily a test failure): %v", err)
	}

	if status, ok := conditionStatus(buildCR.Status.Conditions, "BuildPrerequisitesCreated"); !ok || status != metav1.ConditionTrue {
		t.Errorf("BuildPrerequisitesCreated condition = (%v, found=%v), want (True, true)", status, ok)
	}
}

func TestBuildDomain_Handle_Delete_ResolutionFailure(t *testing.T) {
	buildCR := newBuildCR(nil)
	d, _ := newTestDomain(t, buildCR)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: buildCR})
	if err == nil {
		t.Fatal("Handle() delete with empty contract = nil error, want error")
	}

	if status, ok := conditionStatus(buildCR.Status.Conditions, "BuildDeleted"); !ok || status != metav1.ConditionFalse {
		t.Errorf("BuildDeleted condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestBuildDomain_Handle_Delete_MissingEnvironment(t *testing.T) {
	buildCR := newBuildCR(validBuildContract())
	d, _ := newTestDomain(t, buildCR)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: buildCR})
	if err == nil {
		t.Fatal("Handle() delete with no owning Environment = nil error, want error")
	}

	if status, ok := conditionStatus(buildCR.Status.Conditions, "BuildDeleted"); !ok || status != metav1.ConditionFalse {
		t.Errorf("BuildDeleted condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestBuildDomain_Handle_Delete_Succeeds(t *testing.T) {
	env := newEnvironment()
	buildCR := newBuildCR(validBuildContract())
	d, _ := newTestDomain(t, env, buildCR)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: buildCR}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}

	if status, ok := conditionStatus(buildCR.Status.Conditions, "BuildDeleted"); !ok || status != metav1.ConditionTrue {
		t.Errorf("BuildDeleted condition = (%v, found=%v), want (True, true)", status, ok)
	}
}
