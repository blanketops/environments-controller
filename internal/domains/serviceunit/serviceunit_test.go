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

package serviceunit

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	serviceunitmediator "github.com/blanketops/environments-controller/internal/mediators/serviceunit"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

// Repeated across fixtures below — named to satisfy goconst.
const (
	keyImage      = "image"
	keyType       = "type"
	valTypeStatic = "static"
)

func newServiceUnitCR(contract map[string]any) *environmentsv1alpha1.ServiceUnit {
	su := &environmentsv1alpha1.ServiceUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "su-sample",
			Namespace: "default",
		},
	}
	if contract != nil {
		su.Spec.Contract = testsupport.RawContract(contract)
	}
	return su
}

func validServiceUnitContract() map[string]any {
	return map[string]any{
		keyType:  valTypeStatic,
		keyImage: "ghcr.io/blanketops/app:latest",
	}
}

func newTestDomain(t *testing.T, objs ...client.Object) *ServiceUnitDomain {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()
	med := serviceunitmediator.New(c, testsupport.NewScheme(), log)
	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	return New(med, cache, testsupport.NoopRecorder(), log)
}

func conditionStatus(conds []metav1.Condition, condType string) (metav1.ConditionStatus, bool) {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status, true
		}
	}
	return "", false
}

func TestServiceUnitDomain_GVK(t *testing.T) {
	d := &ServiceUnitDomain{}
	if gvk := d.GVK(); gvk.Kind != "ServiceUnit" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "ServiceUnit")
	}
}

func TestServiceUnitDomain_CanCreate(t *testing.T) {
	d := &ServiceUnitDomain{}
	if !d.CanCreate(&environmentsv1alpha1.ServiceUnit{}) {
		t.Error("CanCreate(*ServiceUnit) = false, want true")
	}
	if d.CanCreate(&environmentsv1alpha1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestServiceUnitDomain_CanDelete(t *testing.T) {
	d := &ServiceUnitDomain{}
	if !d.CanDelete(&environmentsv1alpha1.ServiceUnit{}) {
		t.Error("CanDelete(*ServiceUnit) = false, want true")
	}
	if d.CanDelete(&environmentsv1alpha1.Build{}) {
		t.Error("CanDelete(*Build) = true, want false")
	}
}

func TestServiceUnitDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newServiceUnitCR(map[string]any{keyType: valTypeStatic, keyImage: "a"}),
			newObj: newServiceUnitCR(map[string]any{keyType: valTypeStatic, keyImage: "b"}),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newServiceUnitCR(validServiceUnitContract()),
			newObj: newServiceUnitCR(validServiceUnitContract()),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentsv1alpha1.Build{},
			newObj: newServiceUnitCR(validServiceUnitContract()),
			want:   false,
		},
	}

	d := &ServiceUnitDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceUnitDomain_Handle_InvalidObject(t *testing.T) {
	d := &ServiceUnitDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentsv1alpha1.Build{}}); err == nil {
		t.Fatal("Handle() with non-ServiceUnit object = nil error, want error")
	}
}

func TestServiceUnitDomain_Handle_ResolutionFailure(t *testing.T) {
	su := newServiceUnitCR(nil)
	d := newTestDomain(t, su)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: su})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(su.Status.Conditions, "ServiceUnitResolved"); !ok || status != metav1.ConditionFalse {
		t.Errorf("ServiceUnitResolved condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestServiceUnitDomain_Handle_Succeeds(t *testing.T) {
	su := newServiceUnitCR(validServiceUnitContract())
	d := newTestDomain(t, su)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: su}); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}

	for _, condType := range []string{"ServiceUnitResolved", "ServiceUnitPrerequisitesReady", "ServiceUnitReady"} {
		if status, ok := conditionStatus(su.Status.Conditions, condType); !ok || status != metav1.ConditionTrue {
			t.Errorf("%s condition = (%v, found=%v), want (True, true)", condType, status, ok)
		}
	}
}

// Handle has no CmdDelete branch — the same resolve/prerequisites/ready
// flow runs unconditionally regardless of cmd.Type. This test documents
// that behavior explicitly, so a future CmdDelete case added here doesn't
// silently change delete semantics without a test noticing.
func TestServiceUnitDomain_Handle_DeleteRunsSameFlow(t *testing.T) {
	su := newServiceUnitCR(validServiceUnitContract())
	d := newTestDomain(t, su)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: su}); err != nil {
		t.Fatalf("Handle() with CmdDelete = %v, want nil", err)
	}
	if status, ok := conditionStatus(su.Status.Conditions, "ServiceUnitReady"); !ok || status != metav1.ConditionTrue {
		t.Errorf("ServiceUnitReady condition = (%v, found=%v), want (True, true)", status, ok)
	}
}
