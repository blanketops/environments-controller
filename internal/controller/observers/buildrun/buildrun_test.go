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

package buildrun

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	buildv1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	"github.com/blanketops/environments/pkg/apis/build/application"
	"github.com/blanketops/environments/pkg/apis/build/domain"
	"github.com/go-logr/logr"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

const (
	testNamespace = "default"
	testBuildName = "app-build"
	testImage     = "ghcr.io/acme/app:1111111"
	testDigest    = "sha256:c02393e2773174f790cbf0262a8954a2c9aa19812883d8ea7c5924fecccf4509"
	testRunName   = "run-1"
)

var testEpoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newBuild(contract *domain.BuildStatus) *buildv1.Build {
	b := &buildv1.Build{ObjectMeta: metav1.ObjectMeta{Name: testBuildName, Namespace: testNamespace}}
	if contract != nil {
		raw, _ := json.Marshal(contract)
		b.Status.Contract = runtime.RawExtension{Raw: raw}
	}
	return b
}

// newBuildRun returns a BuildRun for the test Build, created age after
// testEpoch. succeeded is nil for a run that has not finished.
func newBuildRun(name string, age time.Duration, succeeded *bool, image, digest string) *shipwrightv1alpha1.BuildRun {
	br := &shipwrightv1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testNamespace,
			CreationTimestamp: metav1.NewTime(testEpoch.Add(age)),
			Labels: map[string]string{
				"build.blanketops.dev/name": testBuildName,
				"build-hash":                name,
			},
		},
	}
	if succeeded != nil {
		status := corev1.ConditionFalse
		if *succeeded {
			status = corev1.ConditionTrue
		}
		br.Status.Conditions = shipwrightv1alpha1.Conditions{{
			Type: shipwrightv1alpha1.Succeeded, Status: status, Reason: "Done", Message: "buildrun finished",
		}}
	}
	if image != "" {
		br.Status.BuildSpec = &shipwrightv1alpha1.BuildSpec{Output: shipwrightv1alpha1.Image{Image: image}}
	}
	if digest != "" {
		br.Status.Output = &shipwrightv1alpha1.Output{Digest: digest}
	}
	return br
}

func reconcileRun(t *testing.T, c client.Client, name string) {
	t.Helper()
	r := &Reconciler{Client: c, Status: application.NewStatusWriter(c, logr.Discard())}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: testNamespace, Name: name}}); err != nil {
		t.Fatalf("Reconcile(%s): %v", name, err)
	}
}

func readStatus(t *testing.T, c client.Client) (domain.BuildStatus, []metav1.Condition, bool) {
	t.Helper()
	var b buildv1.Build
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testBuildName}, &b); err != nil {
		t.Fatalf("get build: %v", err)
	}
	var st domain.BuildStatus
	if len(b.Status.Contract.Raw) == 0 {
		return st, b.Status.Conditions, false
	}
	if err := json.Unmarshal(b.Status.Contract.Raw, &st); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	return st, b.Status.Conditions, true
}

func hasCondition(conds []metav1.Condition, condType string) bool {
	for _, c := range conds {
		if c.Type == condType {
			return true
		}
	}
	return false
}

func TestReconcile_SuccessRecordsImageWithDigest(t *testing.T) {
	c := testsupport.NewFakeClient(newBuild(nil), newBuildRun(testRunName, 0, new(true), testImage, testDigest))

	reconcileRun(t, c, testRunName)

	st, conds, ok := readStatus(t, c)
	if !ok {
		t.Fatal("status.contract was not written")
	}
	if want := testImage + "@" + testDigest; st.Image != want {
		t.Errorf("Image = %q, want %q", st.Image, want)
	}
	if !st.Success || !st.Triggered || st.ExecutionRef != testRunName || st.BuildHash != testRunName {
		t.Errorf("unexpected contract: %+v", st)
	}
	if !hasCondition(conds, conditionBuildSuccess) {
		t.Errorf("BuildSuccess condition missing: %+v", conds)
	}
}

func TestReconcile_SuccessWithoutDigestRecordsTaggedImage(t *testing.T) {
	c := testsupport.NewFakeClient(newBuild(nil), newBuildRun(testRunName, 0, new(true), testImage, ""))

	reconcileRun(t, c, testRunName)

	st, _, _ := readStatus(t, c)
	if st.Image != testImage {
		t.Errorf("Image = %q, want %q", st.Image, testImage)
	}
}

func TestReconcile_FailureKeepsLastPushedImage(t *testing.T) {
	previous := "ghcr.io/acme/app:0000000@sha256:previous"
	c := testsupport.NewFakeClient(
		newBuild(&domain.BuildStatus{Triggered: true, Success: true, ExecutionRef: "run-0", Image: previous}),
		newBuildRun(testRunName, 0, new(false), testImage, ""),
	)

	reconcileRun(t, c, testRunName)

	st, conds, _ := readStatus(t, c)
	if st.Image != previous {
		t.Errorf("Image = %q, want the previous image %q", st.Image, previous)
	}
	if st.Success || st.ExecutionRef != testRunName {
		t.Errorf("unexpected contract: %+v", st)
	}
	if !hasCondition(conds, conditionBuildFailed) {
		t.Errorf("BuildFailed condition missing: %+v", conds)
	}
}

func TestReconcile_SuccessWithoutSpecSnapshotKeepsPreviousImage(t *testing.T) {
	previous := "ghcr.io/acme/app:0000000@sha256:previous"
	c := testsupport.NewFakeClient(
		newBuild(&domain.BuildStatus{Triggered: true, Success: true, ExecutionRef: "run-0", Image: previous}),
		newBuildRun(testRunName, 0, new(true), "", testDigest),
	)

	reconcileRun(t, c, testRunName)

	st, _, _ := readStatus(t, c)
	if st.Image != previous {
		t.Errorf("Image = %q, want the previous image %q", st.Image, previous)
	}
	if !st.Success || st.ExecutionRef != testRunName {
		t.Errorf("unexpected contract: %+v", st)
	}
}

func TestReconcile_OlderRunDoesNotOverwriteNewer(t *testing.T) {
	newer := testImage + "@" + testDigest
	c := testsupport.NewFakeClient(
		newBuild(nil),
		newBuildRun("run-old", 0, new(false), "ghcr.io/acme/app:0000000", ""),
		newBuildRun("run-new", time.Minute, new(true), testImage, testDigest),
	)

	// Order mirrors a controller restart: the newer run is seen first.
	reconcileRun(t, c, "run-new")
	reconcileRun(t, c, "run-old")

	st, conds, _ := readStatus(t, c)
	if st.ExecutionRef != "run-new" || !st.Success || st.Image != newer {
		t.Errorf("contract was overwritten by the older run: %+v", st)
	}
	if hasCondition(conds, conditionBuildFailed) {
		t.Errorf("older run wrote BuildFailed: %+v", conds)
	}
}

func TestReconcile_SkipsRunsThatDoNotApply(t *testing.T) {
	unlabelled := newBuildRun("run-unlabelled", 0, new(true), testImage, testDigest)
	unlabelled.Labels = nil

	c := testsupport.NewFakeClient(
		newBuild(nil),
		newBuildRun("run-pending", 0, nil, testImage, ""),
		unlabelled,
	)

	for _, name := range []string{"run-pending", "run-unlabelled", "run-missing"} {
		reconcileRun(t, c, name)
	}

	if _, conds, ok := readStatus(t, c); ok || len(conds) != 0 {
		t.Errorf("status was written for a run that does not apply: contract=%v conditions=%+v", ok, conds)
	}
}

func TestPushedImage(t *testing.T) {
	tests := []struct {
		name string
		br   *shipwrightv1alpha1.BuildRun
		want string
	}{
		{name: "image and digest", br: newBuildRun("r", 0, new(true), testImage, testDigest), want: testImage + "@" + testDigest},
		{name: "image only", br: newBuildRun("r", 0, new(true), testImage, ""), want: testImage},
		{name: "no spec snapshot", br: newBuildRun("r", 0, new(true), "", testDigest), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pushedImage(tt.br); got != tt.want {
				t.Errorf("pushedImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func outcomeConditions(conds []metav1.Condition) []string {
	var got []string
	for _, c := range conds {
		if c.Type == conditionBuildSuccess || c.Type == conditionBuildFailed {
			got = append(got, c.Type)
		}
	}
	return got
}

// TestReconcile_LatestOutcomeReplacesThePrevious follows a Build through a
// failed run, a successful retry and a later failure. Only the outcome of the
// newest run may remain, and conditions the observer does not own stay.
func TestReconcile_LatestOutcomeReplacesThePrevious(t *testing.T) {
	build := newBuild(nil)
	build.Status.Conditions = []metav1.Condition{{
		Type: "BuildReady", Status: metav1.ConditionTrue, Reason: "BuildReady", Message: "Build dispatched",
	}}
	c := testsupport.NewFakeClient(build, newBuildRun("run-a", 0, new(false), testImage, ""))

	steps := []struct {
		run       string
		age       time.Duration
		succeeded bool
		want      string
	}{
		{run: "run-a", succeeded: false, want: conditionBuildFailed},
		{run: "run-b", age: time.Minute, succeeded: true, want: conditionBuildSuccess},
		{run: "run-c", age: 2 * time.Minute, succeeded: false, want: conditionBuildFailed},
	}

	for i, step := range steps {
		if i > 0 {
			br := newBuildRun(step.run, step.age, new(step.succeeded), testImage, testDigest)
			if err := c.Create(context.Background(), br); err != nil {
				t.Fatalf("create %s: %v", step.run, err)
			}
		}
		reconcileRun(t, c, step.run)

		_, conds, _ := readStatus(t, c)
		got := outcomeConditions(conds)
		if len(got) != 1 || got[0] != step.want {
			t.Fatalf("after %s: outcome conditions = %v, want only %s", step.run, got, step.want)
		}
		if !hasCondition(conds, "BuildReady") {
			t.Fatalf("after %s: BuildReady was removed: %+v", step.run, conds)
		}
	}

	// The image of the last successful run is still recorded after the
	// later failure.
	st, _, _ := readStatus(t, c)
	if want := testImage + "@" + testDigest; st.Image != want {
		t.Errorf("Image = %q, want %q", st.Image, want)
	}
}
