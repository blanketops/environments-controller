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

package packages

import (
	"context"
	"testing"

	environmentv1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	pkgapi "github.com/blanketops/environments/pkg/apis/packages/api"
	pkgapplication "github.com/blanketops/environments/pkg/apis/packages/application"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pkgmediator "github.com/blanketops/environments-controller/internal/mediators/packages"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const testAppName = "app-sample"

// Repeated across fixtures below — named to satisfy goconst.
const keyPackageName = "packageName"

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

func newPackageCR(contract map[string]any) *environmentv1.Package {
	p := &environmentv1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "package-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	if contract != nil {
		p.Spec.Contract = testsupport.RawContract(contract)
	}
	return p
}

func validPackageContract() map[string]any {
	return map[string]any{
		keyPackageName:   "app",
		"packageVersion": "1.0.0",
		"packageRepository": map[string]any{
			"url": "oci://ghcr.io/blanketops/packages/app",
		},
		// stateRepo is documented-optional in resolution (resolve.go:64:
		// "not all packages track state via GitOps"), but the environments
		// library's BuildPackageIntent (pkg/intent/package/builder.go:58-60)
		// unconditionally dereferences spec.StateRepository — a real
		// external-library nil-pointer bug found while writing this test.
		// Included here to route around it rather than fix a different
		// repo mid-coverage-task; see the mirrored, already-fixed bug in
		// this repo's internal/mediators/packages/package.go for the same
		// class of issue.
		"stateRepo": map[string]any{
			"url": "https://github.com/blanketops/app-state.git",
		},
	}
}

func newTestDomain(t *testing.T, objs ...client.Object) *PackageDomain {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()
	rawRec := testsupport.NoopRawRecorder()

	med := pkgmediator.New(c, testsupport.NewScheme(), log, rawRec)

	mapper := pkgapplication.NewMapper()
	statusWriter := pkgapplication.NewStatusWriter(c, log)
	backend := pkgapplication.NewBackendSelector(pkgapi.NewPackageProvider(c, testsupport.NewScheme(), log, rawRec))
	service := pkgapplication.NewPackageService(mapper, backend, statusWriter)

	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	return New(med, service, cache, testsupport.NoopRecorder(), log)
}

func conditionStatus(conds []metav1.Condition, condType string) (metav1.ConditionStatus, bool) {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status, true
		}
	}
	return "", false
}

func TestPackageDomain_GVK(t *testing.T) {
	d := &PackageDomain{}
	if gvk := d.GVK(); gvk.Kind != "Package" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "Package")
	}
}

func TestPackageDomain_CanCreate(t *testing.T) {
	d := &PackageDomain{}
	if !d.CanCreate(&environmentv1.Package{}) {
		t.Error("CanCreate(*Package) = false, want true")
	}
	if d.CanCreate(&environmentv1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestPackageDomain_CanDelete(t *testing.T) {
	d := &PackageDomain{}
	if !d.CanDelete(&environmentv1.Package{}) {
		t.Error("CanDelete(*Package) = false, want true")
	}
	if d.CanDelete(&environmentv1.Build{}) {
		t.Error("CanDelete(*Build) = true, want false")
	}
}

func TestPackageDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newPackageCR(map[string]any{keyPackageName: "a"}),
			newObj: newPackageCR(map[string]any{keyPackageName: "b"}),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newPackageCR(validPackageContract()),
			newObj: newPackageCR(validPackageContract()),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentv1.Build{},
			newObj: newPackageCR(validPackageContract()),
			want:   false,
		},
	}

	d := &PackageDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPackageDomain_Handle_InvalidObject(t *testing.T) {
	d := &PackageDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentv1.Build{}}); err == nil {
		t.Fatal("Handle() with non-Package object = nil error, want error")
	}
}

func TestPackageDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	pkg := newPackageCR(nil)
	d := newTestDomain(t, pkg)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: pkg})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(pkg.Status.Conditions, "PackageResolved"); !ok || status != metav1.ConditionFalse {
		t.Errorf("PackageResolved condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestPackageDomain_Handle_Create_MissingEnvironment(t *testing.T) {
	pkg := newPackageCR(validPackageContract())
	d := newTestDomain(t, pkg)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: pkg})
	if err == nil {
		t.Fatal("Handle() with no owning Environment = nil error, want error")
	}
	if status, ok := conditionStatus(pkg.Status.Conditions, "PackagePrerequisitesCreateFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("PackagePrerequisitesCreateFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestPackageDomain_Handle_Create_Succeeds(t *testing.T) {
	env := newEnvironment()
	pkg := newPackageCR(validPackageContract())
	d := newTestDomain(t, env, pkg)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: pkg}); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}

	for _, condType := range []string{"PackageResolved", "PackageCached", "PackagePrerequisitesCreated", "PackageIntentBuilt", "PackageTriggered"} {
		if status, ok := conditionStatus(pkg.Status.Conditions, condType); !ok || status != metav1.ConditionTrue {
			t.Errorf("%s condition = (%v, found=%v), want (True, true)", condType, status, ok)
		}
	}
}

func TestPackageDomain_Handle_Delete_ResolutionFailure(t *testing.T) {
	pkg := newPackageCR(nil)
	d := newTestDomain(t, pkg)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: pkg})
	if err == nil {
		t.Fatal("Handle() delete with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(pkg.Status.Conditions, "PackageDeleted"); !ok || status != metav1.ConditionFalse {
		t.Errorf("PackageDeleted condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestPackageDomain_Handle_Delete_Succeeds(t *testing.T) {
	env := newEnvironment()
	pkg := newPackageCR(validPackageContract())
	d := newTestDomain(t, env, pkg)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: pkg}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}
	if status, ok := conditionStatus(pkg.Status.Conditions, "PackageDeleted"); !ok || status != metav1.ConditionTrue {
		t.Errorf("PackageDeleted condition = (%v, found=%v), want (True, true)", status, ok)
	}
}
