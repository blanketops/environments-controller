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
	"encoding/json"
	"testing"
	"time"

	kappctrlv1alpha1 "carvel.dev/kapp-controller/pkg/apis/kappctrl/v1alpha1"
	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	"github.com/blanketops/environments/pkg/apis/packages/application"
	"github.com/blanketops/environments/pkg/apis/packages/domain"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

const (
	testNamespace = "default"
	testPackage   = "package-sample"
	condSucceeded = "Succeeded"
)

var appKey = client.ObjectKey{Namespace: testNamespace, Name: testPackage}

func newPackage() *environmentsv1alpha1.Package {
	p := &environmentsv1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: testPackage, Namespace: testNamespace, UID: "uid-package"},
	}
	// A condition set by the Package reconciler, which the observer must keep.
	p.Status.Conditions = []metav1.Condition{{
		Type: "PackageTriggered", Status: metav1.ConditionTrue, Reason: "ExecutionRequested", Message: "requested",
	}}
	return p
}

// newApp returns the kapp App for the test Package, owned by it unless owned
// is false, reporting the given conditions.
func newApp(owned bool, useful string, conds ...kappctrlv1alpha1.Condition) *kappctrlv1alpha1.App {
	app := &kappctrlv1alpha1.App{ObjectMeta: metav1.ObjectMeta{Name: testPackage, Namespace: testNamespace}}
	if owned {
		app.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: environmentsv1alpha1.GroupVersion.String(), Kind: "Package",
			Name: testPackage, UID: "uid-package", Controller: new(true),
		}}
	}
	app.Status.Conditions = conds
	app.Status.UsefulErrorMessage = useful
	return app
}

func cond(t kappctrlv1alpha1.ConditionType, msg string) kappctrlv1alpha1.Condition {
	return kappctrlv1alpha1.Condition{Type: t, Status: corev1.ConditionTrue, Message: msg}
}

func observe(t *testing.T, c client.Client) {
	t.Helper()
	r := &Reconciler{Client: c, Status: application.NewStatusWriter(c, logr.Discard())}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: appKey}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func readPackage(t *testing.T, c client.Client) (*environmentsv1alpha1.Package, domain.PackageStatus) {
	t.Helper()
	p := &environmentsv1alpha1.Package{}
	if err := c.Get(context.Background(), appKey, p); err != nil {
		t.Fatalf("get package: %v", err)
	}
	var st domain.PackageStatus
	if len(p.Status.Contract.Raw) > 0 {
		if err := json.Unmarshal(p.Status.Contract.Raw, &st); err != nil {
			t.Fatalf("decode contract: %v", err)
		}
	}
	return p, st
}

// TestReconcile_WritesWhatTheAppReports covers the outcome each App state
// leaves on the owning Package.
func TestReconcile_WritesWhatTheAppReports(t *testing.T) {
	tests := []struct {
		name        string
		app         *kappctrlv1alpha1.App
		wantStatus  metav1.ConditionStatus
		wantPhase   domain.PackagePhase
		wantSuccess bool
		wantMessage string
	}{
		{
			name:       "nothing reported yet",
			app:        newApp(true, ""),
			wantStatus: metav1.ConditionUnknown, wantPhase: domain.PackagePhasePending,
		},
		{
			name:       "reconciling",
			app:        newApp(true, "", cond(kappctrlv1alpha1.Reconciling, "")),
			wantStatus: metav1.ConditionUnknown, wantPhase: domain.PackagePhasePending,
		},
		{
			name:       "fetch failed",
			app:        newApp(true, "Host key verification failed", cond(kappctrlv1alpha1.ReconcileFailed, "Fetching resources: Error")),
			wantStatus: metav1.ConditionFalse, wantPhase: domain.PackagePhaseFailed, wantMessage: "Host key verification failed",
		},
		{
			name:       "applied",
			app:        newApp(true, "", cond(kappctrlv1alpha1.ReconcileSucceeded, "")),
			wantStatus: metav1.ConditionTrue, wantPhase: domain.PackagePhaseSucceeded, wantSuccess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testsupport.NewFakeClient(newPackage(), tt.app)
			observe(t, c)

			p, st := readPackage(t, c)
			if st.Phase != tt.wantPhase || st.Success != tt.wantSuccess || st.Message != tt.wantMessage {
				t.Errorf("contract = %+v, want phase %s success %v message %q", st, tt.wantPhase, tt.wantSuccess, tt.wantMessage)
			}
			succeeded := apimeta.FindStatusCondition(p.Status.Conditions, condSucceeded)
			if succeeded == nil || succeeded.Status != tt.wantStatus {
				t.Errorf("%s = %+v, want %s", condSucceeded, succeeded, tt.wantStatus)
			}
			if !apimeta.IsStatusConditionTrue(p.Status.Conditions, "PackageTriggered") {
				t.Errorf("a condition set by the Package reconciler was lost: %+v", p.Status.Conditions)
			}
		})
	}
}

// TestReconcile_FollowsTheAppOverTime moves one App from failed to applied:
// the Package ends on the latest outcome, with a single Succeeded condition.
func TestReconcile_FollowsTheAppOverTime(t *testing.T) {
	c := testsupport.NewFakeClient(newPackage(), newApp(true, "fetch failed", cond(kappctrlv1alpha1.ReconcileFailed, "Fetching resources: Error")))
	observe(t, c)
	if p, _ := readPackage(t, c); !apimeta.IsStatusConditionFalse(p.Status.Conditions, condSucceeded) {
		t.Fatalf("after the failure: %+v", p.Status.Conditions)
	}

	app := &kappctrlv1alpha1.App{}
	if err := c.Get(context.Background(), appKey, app); err != nil {
		t.Fatalf("get app: %v", err)
	}
	app.Status.Conditions = []kappctrlv1alpha1.Condition{cond(kappctrlv1alpha1.ReconcileSucceeded, "")}
	app.Status.UsefulErrorMessage = ""
	if err := c.Update(context.Background(), app); err != nil {
		t.Fatalf("update app: %v", err)
	}
	observe(t, c)

	p, st := readPackage(t, c)
	if !st.Success || st.Phase != domain.PackagePhaseSucceeded {
		t.Errorf("contract = %+v, want succeeded", st)
	}
	count := 0
	for _, cd := range p.Status.Conditions {
		if cd.Type == condSucceeded {
			count++
		}
	}
	if count != 1 || !apimeta.IsStatusConditionTrue(p.Status.Conditions, condSucceeded) {
		t.Errorf("conditions = %+v, want one %s=True", p.Status.Conditions, condSucceeded)
	}
}

// TestReconcile_IgnoresWhatIsNotItsToReport covers Apps and Packages the
// observer must leave alone.
func TestReconcile_IgnoresWhatIsNotItsToReport(t *testing.T) {
	failed := cond(kappctrlv1alpha1.ReconcileFailed, "Fetching resources: Error")

	foreignOwner := newApp(true, "", failed)
	foreignOwner.OwnerReferences[0].Kind = "Deployment"

	deleting := newPackage()
	now := metav1.NewTime(time.Now())
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"environments.blanketops.dev/package-finalizer"}

	tests := []struct {
		name string
		objs []client.Object
	}{
		{name: "app without an owner", objs: []client.Object{newPackage(), newApp(false, "", failed)}},
		{name: "app owned by another kind", objs: []client.Object{newPackage(), foreignOwner}},
		{name: "owning package is gone", objs: []client.Object{newApp(true, "", failed)}},
		{name: "owning package is being deleted", objs: []client.Object{deleting, newApp(true, "", failed)}},
		{name: "app is gone", objs: []client.Object{newPackage()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testsupport.NewFakeClient(tt.objs...)
			observe(t, c)

			p := &environmentsv1alpha1.Package{}
			if err := c.Get(context.Background(), appKey, p); err != nil {
				return // no Package to check
			}
			if apimeta.FindStatusCondition(p.Status.Conditions, condSucceeded) != nil || len(p.Status.Contract.Raw) != 0 {
				t.Errorf("status was written: conditions=%+v contract=%s", p.Status.Conditions, p.Status.Contract.Raw)
			}
		})
	}
}
