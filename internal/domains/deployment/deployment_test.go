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

package deployment

import (
	"context"
	"testing"

	environmentv1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	deploymentmediator "github.com/blanketops/environments-controller/internal/mediators/deployment"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const testAppName = "app-sample"

func newEnvironment() *environmentv1.Environment {
	return &environmentv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAppName,
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
				"environments.blanketops.dev/type": "dev",
			},
		},
		Spec: environmentv1.EnvironmentSpec{
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

func newDeploymentCR(contract map[string]any) *environmentv1.Deployment {
	d := &environmentv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deployment-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	if contract != nil {
		d.Spec.Contract = testsupport.RawContract(contract)
	}
	return d
}

func validDeploymentContract(serviceUnits ...string) map[string]any {
	if len(serviceUnits) == 0 {
		serviceUnits = []string{"su-sample"}
	}
	units := make([]any, 0, len(serviceUnits))
	for _, s := range serviceUnits {
		units = append(units, s)
	}
	return map[string]any{
		"serviceUnits": units,
		"runtime":      "kubernetes",
		"strategy":     "Rolling",
	}
}

func newServiceUnit(name string) *environmentv1.ServiceUnit {
	return &environmentv1.ServiceUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
		Spec: environmentv1.ServiceUnitSpec{
			Contract: testsupport.RawContract(map[string]any{
				"type":  "static",
				"image": "ghcr.io/blanketops/app:latest",
			}),
		},
	}
}

// newTestDomain wires a real DeployDomain (mediator + no application-layer
// deployService) against a fake client. deployService is nil, which is a
// real, supported production configuration (Handle guards it with
// `if d.deployService != nil`) — omitting it here keeps these tests focused
// on the domain's own orchestration (resolution, prerequisites, cross-CR
// ServiceUnit resolution) rather than re-testing the deployment application
// layer's reconciliation-executor/Flux Kustomize machinery.
func newTestDomain(t *testing.T, objs ...client.Object) (*DeployDomain, client.Client) {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()

	med := deploymentmediator.New(c, testsupport.NewScheme(), log, testsupport.NoopRawRecorder())
	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}

	d := New(med, nil, cache, c, testsupport.NoopRecorder(), log)
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

func TestDeployDomain_GVK(t *testing.T) {
	d := &DeployDomain{}
	gvk := d.GVK()
	if gvk.Kind != "Deployment" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "Deployment")
	}
}

func TestDeployDomain_CanCreate(t *testing.T) {
	d := &DeployDomain{}
	if !d.CanCreate(&environmentv1.Deployment{}) {
		t.Error("CanCreate(*Deployment) = false, want true")
	}
	if d.CanCreate(&environmentv1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestDeployDomain_CanDelete(t *testing.T) {
	d := &DeployDomain{}
	if !d.CanDelete(&environmentv1.Deployment{}) {
		t.Error("CanDelete(*Deployment) = false, want true")
	}
	if d.CanDelete(&environmentv1.Build{}) {
		t.Error("CanDelete(*Build) = true, want false")
	}
}

func TestDeployDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newDeploymentCR(validDeploymentContract("a")),
			newObj: newDeploymentCR(validDeploymentContract("b")),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newDeploymentCR(validDeploymentContract("a")),
			newObj: newDeploymentCR(validDeploymentContract("a")),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentv1.Build{},
			newObj: newDeploymentCR(validDeploymentContract()),
			want:   false,
		},
	}

	d := &DeployDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeployDomain_Handle_InvalidObject(t *testing.T) {
	d := &DeployDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentv1.Build{}}); err == nil {
		t.Fatal("Handle() with non-Deployment object = nil error, want error")
	}
}

func TestDeployDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	depl := newDeploymentCR(nil)
	d, _ := newTestDomain(t, depl)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: depl})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(depl.Status.Conditions, "DeploymentResolveFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("DeploymentResolveFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestDeployDomain_Handle_Create_MissingEnvironment(t *testing.T) {
	depl := newDeploymentCR(validDeploymentContract())
	d, _ := newTestDomain(t, depl)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: depl})
	if err == nil {
		t.Fatal("Handle() with no owning Environment = nil error, want error")
	}
	if status, ok := conditionStatus(depl.Status.Conditions, "DeploymentPrerequisitesCreateFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("DeploymentPrerequisitesCreateFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestDeployDomain_Handle_Create_ServiceUnitMissing(t *testing.T) {
	env := newEnvironment()
	depl := newDeploymentCR(validDeploymentContract("su-missing"))
	d, _ := newTestDomain(t, env, depl)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: depl})
	if err == nil {
		t.Fatal("Handle() with unresolvable ServiceUnit = nil error, want error")
	}
	if status, ok := conditionStatus(depl.Status.Conditions, "ServiceUnitResolved"); !ok || status != metav1.ConditionFalse {
		t.Errorf("ServiceUnitResolved condition = (%v, found=%v), want (False, true)", status, ok)
	}
	// Prerequisites must have succeeded before service-unit resolution ran.
	if status, ok := conditionStatus(depl.Status.Conditions, "DeploymentPrerequisitesCreated"); !ok || status != metav1.ConditionTrue {
		t.Errorf("DeploymentPrerequisitesCreated condition = (%v, found=%v), want (True, true)", status, ok)
	}
}

func TestDeployDomain_Handle_Create_Succeeds(t *testing.T) {
	env := newEnvironment()
	su := newServiceUnit("su-sample")
	depl := newDeploymentCR(validDeploymentContract("su-sample"))
	d, _ := newTestDomain(t, env, su, depl)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: depl}); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}

	// deployService is nil in this test's wiring, so the domain must still
	// reach and set DeploymentSucceeded — the nil-guard around
	// deployService.Reconcile must not skip the terminal success condition.
	if status, ok := conditionStatus(depl.Status.Conditions, "DeploymentSucceeded"); !ok || status != metav1.ConditionTrue {
		t.Errorf("DeploymentSucceeded condition = (%v, found=%v), want (True, true)", status, ok)
	}
}

func TestDeployDomain_Handle_Delete_ResolutionFailure(t *testing.T) {
	depl := newDeploymentCR(nil)
	d, _ := newTestDomain(t, depl)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: depl})
	if err == nil {
		t.Fatal("Handle() delete with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(depl.Status.Conditions, "DeploymentDeleted"); !ok || status != metav1.ConditionFalse {
		t.Errorf("DeploymentDeleted condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestDeployDomain_Handle_Delete_Succeeds(t *testing.T) {
	env := newEnvironment()
	depl := newDeploymentCR(validDeploymentContract())
	d, _ := newTestDomain(t, env, depl)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: depl}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}
	if status, ok := conditionStatus(depl.Status.Conditions, "DeploymentDeleted"); !ok || status != metav1.ConditionTrue {
		t.Errorf("DeploymentDeleted condition = (%v, found=%v), want (True, true)", status, ok)
	}
}
