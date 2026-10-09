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
	"time"

	kappctrlv1alpha1 "carvel.dev/kapp-controller/pkg/apis/kappctrl/v1alpha1"
	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/engine"
	"github.com/blanketops/environments/core/registry"
	pkgProvider "github.com/blanketops/environments/pkg/apis/packages/api"
	pkgApp "github.com/blanketops/environments/pkg/apis/packages/application"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"

	pkgDomain "github.com/blanketops/environments-controller/internal/domains/packages"
	pkgMediator "github.com/blanketops/environments-controller/internal/mediators/packages"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const (
	pkgTestApp   = "app-sample"
	pkgTestName  = "package-sample"
	pkgNamespace = "default"
	keyVersion   = "version"
	pkgVersion   = "1.0.0"
)

var pkgLabels = map[string]string{
	"environments.blanketops.dev/name": pkgTestApp,
	"environments.blanketops.dev/type": "dev",
}

func newPackageEnvironment() *environmentsv1alpha1.Environment {
	return &environmentsv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: pkgTestApp, Namespace: pkgNamespace, Labels: pkgLabels},
		Spec: environmentsv1alpha1.EnvironmentSpec{Contract: testsupport.RawContract(map[string]any{
			"applicationName": pkgTestApp, "branch": "main", "gitOwner": "blanketops", "environmentType": "dev", keyVersion: "v1",
		})},
	}
}

func newPackage(contract map[string]any) *environmentsv1alpha1.Package {
	p := &environmentsv1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: pkgTestName, Namespace: pkgNamespace, Labels: pkgLabels, UID: "uid-package"},
	}
	if contract != nil {
		p.Spec.Contract = testsupport.RawContract(contract)
	}
	return p
}

func validPackageContract() map[string]any {
	return map[string]any{
		"name":       "app",
		keyVersion:   pkgVersion,
		"repository": map[string]any{"url": "git@github.com:example-org/packages.git", "credentialsSecret": "packages-creds", "ref": "origin/main"},
		"stateRepository": map[string]any{
			"url": "git@github.com:example-org/state.git", "ref": "master", "cloneSecret": "state-creds",
		},
	}
}

// newPackageReconciler wires the reconciler the way SetupWithManager does,
// against c.
func newPackageReconciler(c client.Client) *PackageReconciler {
	log := logr.Discard()
	raw := testsupport.NoopRawRecorder()
	scheme := testsupport.NewScheme()

	service := pkgApp.NewPackageService(
		pkgApp.NewMapper(),
		pkgApp.NewBackendSelector(pkgProvider.NewApplicationProvider(c, scheme, log, raw)),
		pkgApp.NewStatusWriter(c, log),
	)
	reg := registry.NewRegistry()
	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	reg.RegisterDomain(environmentsv1alpha1.GroupVersion.WithKind("Package"),
		pkgDomain.New(pkgMediator.New(c, scheme, log, raw), service, cache, testsupport.NoopRecorder(), log))

	return &PackageReconciler{
		Client: c, Scheme: scheme, Log: log, Recorder: raw,
		Runtime: &runtimeinfra.Runtime{
			Cache: cache, Events: testsupport.NoopRecorder(), Registry: reg,
			Engine: engine.NewEngine(reg, log), Log: log,
		},
	}
}

var pkgKey = client.ObjectKey{Namespace: pkgNamespace, Name: pkgTestName}

func reconcilePackage(t *testing.T, r *PackageReconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pkgKey})
	return err
}

func getPackage(t *testing.T, c client.Client) (*environmentsv1alpha1.Package, error) {
	t.Helper()
	p := &environmentsv1alpha1.Package{}
	return p, c.Get(context.Background(), pkgKey, p)
}

func externalSecretCount(t *testing.T, c client.Client) int {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "ExternalSecretList"})
	if err := c.List(context.Background(), list, client.InNamespace(pkgNamespace)); err != nil {
		t.Fatalf("list externalsecrets: %v", err)
	}
	return len(list.Items)
}

func TestPackageReconciler_AddsFinalizerThenReconciles(t *testing.T) {
	c := testsupport.NewFakeClient(newPackageEnvironment(), newPackage(validPackageContract()))
	r := newPackageReconciler(c)

	// First pass only adds the finalizer.
	if err := reconcilePackage(t, r); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	p, err := getPackage(t, c)
	if err != nil {
		t.Fatalf("get package: %v", err)
	}
	if !controllerutil.ContainsFinalizer(p, packageFinalizer) {
		t.Fatalf("finalizers = %v, want %s", p.Finalizers, packageFinalizer)
	}
	if err := c.Get(context.Background(), pkgKey, &kappctrlv1alpha1.App{}); !apierrors.IsNotFound(err) {
		t.Fatalf("kapp App after the finalizer pass: err = %v, want not found", err)
	}

	// Second pass does the work.
	if err := reconcilePackage(t, r); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	app := &kappctrlv1alpha1.App{}
	if err := c.Get(context.Background(), pkgKey, app); err != nil {
		t.Fatalf("kapp App: %v", err)
	}
	if len(app.OwnerReferences) != 1 || app.OwnerReferences[0].UID != "uid-package" {
		t.Errorf("App owner references = %+v, want the Package", app.OwnerReferences)
	}
	if got := externalSecretCount(t, c); got != 2 {
		t.Errorf("%d ExternalSecrets, want 2", got)
	}
	p, _ = getPackage(t, c)
	for _, condType := range []string{"PackageResolved", "PackagePrerequisitesCreated", "PackageIntentBuilt", "PackageTriggered"} {
		if !apimeta.IsStatusConditionTrue(p.Status.Conditions, condType) {
			t.Errorf("%s is not True: %+v", condType, p.Status.Conditions)
		}
	}
	if len(p.Status.Contract.Raw) == 0 {
		t.Error("status.contract written by the package service was lost")
	}
}

// TestPackageReconciler_FailureIsRecordedOnThePackage covers a reconcile that
// fails: the reason must be on the Package, not only in the returned error.
func TestPackageReconciler_FailureIsRecordedOnThePackage(t *testing.T) {
	tests := []struct {
		name      string
		objs      []client.Object
		condition string
	}{
		{
			name:      "contract does not resolve",
			objs:      []client.Object{newPackageEnvironment(), newPackage(map[string]any{keyVersion: pkgVersion})},
			condition: "PackageResolved",
		},
		{
			name:      "environment missing",
			objs:      []client.Object{newPackage(validPackageContract())},
			condition: "PackagePrerequisitesCreateFailed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testsupport.NewFakeClient(tt.objs...)
			r := newPackageReconciler(c)
			if err := reconcilePackage(t, r); err != nil {
				t.Fatalf("finalizer pass: %v", err)
			}

			if err := reconcilePackage(t, r); err == nil {
				t.Fatal("Reconcile = nil error, want the failure")
			}
			p, err := getPackage(t, c)
			if err != nil {
				t.Fatalf("get package: %v", err)
			}
			cond := apimeta.FindStatusCondition(p.Status.Conditions, tt.condition)
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Message == "" {
				t.Errorf("%s = %+v, want it False with the reason; all conditions: %+v", tt.condition, cond, p.Status.Conditions)
			}
		})
	}
}

func TestPackageReconciler_DeleteTearsDownAndReleasesTheFinalizer(t *testing.T) {
	tests := []struct {
		name     string
		contract map[string]any
	}{
		{name: "valid package", contract: validPackageContract()},
		{name: "package whose contract does not resolve", contract: map[string]any{keyVersion: pkgVersion}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testsupport.NewFakeClient(newPackageEnvironment(), newPackage(tt.contract))
			r := newPackageReconciler(c)
			_ = reconcilePackage(t, r) // finalizer
			_ = reconcilePackage(t, r) // work, or a recorded failure

			p, err := getPackage(t, c)
			if err != nil {
				t.Fatalf("get package: %v", err)
			}
			if err := c.Delete(context.Background(), p); err != nil {
				t.Fatalf("delete package: %v", err)
			}
			if _, err := getPackage(t, c); err != nil {
				t.Fatalf("package must wait on its finalizer, got: %v", err)
			}

			if err := reconcilePackage(t, r); err != nil {
				t.Fatalf("delete Reconcile: %v", err)
			}

			if _, err := getPackage(t, c); !apierrors.IsNotFound(err) {
				t.Errorf("package after teardown: err = %v, want not found", err)
			}
			if err := c.Get(context.Background(), pkgKey, &kappctrlv1alpha1.App{}); !apierrors.IsNotFound(err) {
				t.Errorf("kapp App after teardown: err = %v, want not found", err)
			}
			if got := externalSecretCount(t, c); got != 0 {
				t.Errorf("%d ExternalSecrets after teardown, want 0", got)
			}

			// A reconcile after the object is gone is a no-op.
			if err := reconcilePackage(t, r); err != nil {
				t.Errorf("Reconcile after deletion: %v", err)
			}
		})
	}
}

func TestDeletionRequested(t *testing.T) {
	live := newPackage(nil)
	deleting := newPackage(nil)
	now := metav1.NewTime(time.Now())
	deleting.DeletionTimestamp = &now

	p := deletionRequested()
	if !p.Update(event.UpdateEvent{ObjectOld: live, ObjectNew: deleting}) {
		t.Error("marking an object for deletion must pass")
	}
	if p.Update(event.UpdateEvent{ObjectOld: live, ObjectNew: live}) {
		t.Error("an unrelated update must not pass")
	}
	if p.Update(event.UpdateEvent{ObjectOld: deleting, ObjectNew: deleting}) {
		t.Error("an update to an object already being deleted must not pass")
	}
	if p.Update(event.UpdateEvent{}) {
		t.Error("an update without objects must not pass")
	}
	if p.Create(event.CreateEvent{Object: live}) || p.Delete(event.DeleteEvent{Object: live}) || p.Generic(event.GenericEvent{Object: live}) {
		t.Error("only updates are passed by this predicate")
	}
}
