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

// This mediator provisions no cross-cutting prerequisites of its own (no
// secrets, no ServiceAccounts, no environment lookup) — EnsurePrerequisites
// only validates and logs. There is deliberately no CleanupPrerequisites
// method at all, matching the domain layer's own lack of a CmdDelete branch
// (see internal/domains/serviceunit). These tests cover the nil-guard and
// the successful no-op path.
package serviceunit

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	serviceunitResolution "github.com/blanketops/environments/resolution/serviceunit/resolve"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

func newResolvedServiceUnit() *serviceunitResolution.ResolvedServiceUnit {
	su := &environmentsv1alpha1.ServiceUnit{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "serviceunit-sample",
			Namespace: "default",
		},
	}
	return &serviceunitResolution.ResolvedServiceUnit{
		ServiceUnit: su,
		Spec: &serviceunitResolution.ResolvedServiceUnitSpec{
			Image:         "ghcr.io/blanketops/app:latest",
			ContainerPort: 8080,
			Size:          1,
		},
	}
}

func TestMediator_EnsurePrerequisites_NilResolved(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard())
	if err := m.EnsurePrerequisites(context.Background(), nil); err == nil {
		t.Fatal("EnsurePrerequisites(nil) = nil error, want error")
	}
}

func TestMediator_EnsurePrerequisites_NilSpec(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard())
	resolved := &serviceunitResolution.ResolvedServiceUnit{
		ServiceUnit: &environmentsv1alpha1.ServiceUnit{ObjectMeta: metav1.ObjectMeta{Name: "serviceunit-sample", Namespace: "default"}},
		Spec:        nil,
	}
	if err := m.EnsurePrerequisites(context.Background(), resolved); err == nil {
		t.Fatal("EnsurePrerequisites() with nil Spec = nil error, want error")
	}
}

func TestMediator_EnsurePrerequisites_Succeeds(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard())
	if err := m.EnsurePrerequisites(context.Background(), newResolvedServiceUnit()); err != nil {
		t.Fatalf("EnsurePrerequisites() = %v, want nil", err)
	}
}

func TestMediator_EnsurePrerequisites_Idempotent(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard())
	resolved := newResolvedServiceUnit()

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("first EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("second EnsurePrerequisites() = %v, want nil (must be idempotent)", err)
	}
}
