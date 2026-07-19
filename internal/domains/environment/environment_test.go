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

package environment

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

// Repeated across fixtures below — named to satisfy goconst.
const keyApplicationName = "applicationName"

func newEnvironmentCR(contract map[string]any) *environmentsv1alpha1.Environment {
	e := &environmentsv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "env-sample",
			Namespace: "default",
		},
	}
	if contract != nil {
		e.Spec.Contract = testsupport.RawContract(contract)
	}
	return e
}

func validEnvironmentContract() map[string]any {
	return map[string]any{
		keyApplicationName: "env-sample",
		"branch":           "main",
		"gitOwner":         "blanketops",
		"environmentType":  "dev",
		"version":          "v1",
	}
}

func newTestDomain(t *testing.T, objs ...client.Object) *EnvironmentDomain {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	return New(c, testsupport.NewScheme(), cache, testsupport.NoopRecorder(), logr.Discard())
}

func conditionStatus(conds []metav1.Condition, condType string) (metav1.ConditionStatus, bool) {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status, true
		}
	}
	return "", false
}

func TestEnvironmentDomain_GVK(t *testing.T) {
	d := &EnvironmentDomain{}
	if gvk := d.GVK(); gvk.Kind != "Environment" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "Environment")
	}
}

func TestEnvironmentDomain_CanCreate(t *testing.T) {
	d := &EnvironmentDomain{}
	if !d.CanCreate(&environmentsv1alpha1.Environment{}) {
		t.Error("CanCreate(*Environment) = false, want true")
	}
	if d.CanCreate(&environmentsv1alpha1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestEnvironmentDomain_CanDelete(t *testing.T) {
	d := &EnvironmentDomain{}
	if !d.CanDelete(&environmentsv1alpha1.Environment{}) {
		t.Error("CanDelete(*Environment) = false, want true")
	}
	if d.CanDelete(&environmentsv1alpha1.Build{}) {
		t.Error("CanDelete(*Build) = true, want false")
	}
}

func TestEnvironmentDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newEnvironmentCR(map[string]any{keyApplicationName: "a"}),
			newObj: newEnvironmentCR(map[string]any{keyApplicationName: "b"}),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newEnvironmentCR(validEnvironmentContract()),
			newObj: newEnvironmentCR(validEnvironmentContract()),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentsv1alpha1.Build{},
			newObj: newEnvironmentCR(validEnvironmentContract()),
			want:   false,
		},
	}

	d := &EnvironmentDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvironmentDomain_Handle_InvalidObject(t *testing.T) {
	d := &EnvironmentDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentsv1alpha1.Build{}}); err == nil {
		t.Fatal("Handle() with non-Environment object = nil error, want error")
	}
}

func TestEnvironmentDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	env := newEnvironmentCR(nil)
	d := newTestDomain(t, env)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: env})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(env.Status.Conditions, "EnvironmentResolveFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("EnvironmentResolveFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestEnvironmentDomain_Handle_Create_Succeeds(t *testing.T) {
	env := newEnvironmentCR(validEnvironmentContract())
	d := newTestDomain(t, env)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: env}); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}

	for _, condType := range []string{"EnvironmentResolved", "EnvironmentCached", "EnvironmentReady"} {
		if status, ok := conditionStatus(env.Status.Conditions, condType); !ok || status != metav1.ConditionTrue {
			t.Errorf("%s condition = (%v, found=%v), want (True, true)", condType, status, ok)
		}
	}
}

func TestEnvironmentDomain_Handle_Delete(t *testing.T) {
	env := newEnvironmentCR(validEnvironmentContract())
	d := newTestDomain(t, env)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: env}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}
	if status, ok := conditionStatus(env.Status.Conditions, "EnvironmentDeleted"); !ok || status != metav1.ConditionTrue {
		t.Errorf("EnvironmentDeleted condition = (%v, found=%v), want (True, true)", status, ok)
	}
}
