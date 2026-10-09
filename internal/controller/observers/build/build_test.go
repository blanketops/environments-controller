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

	buildv1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/blanketops/environments-api/api/events/v1alpha1"
	"github.com/blanketops/environments/pkg/apis/environment/query"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

const (
	testApp       = "app-sample"
	testBuildName = "app-build"
	testNamespace = "default"
	testRef       = "refs/heads/main"
	eventPush     = "push"
	keyTriggers   = "allowedTriggers"
	testSHA       = "1111111aaaaaaa"
)

var appLabels = map[string]string{query.LabelEnvironmentName: testApp}

func newBuild(policy map[string]any) *buildv1.Build {
	contract := map[string]any{
		"image":    "ghcr.io/acme/app:main",
		"source":   map[string]any{"url": "https://github.com/acme/app.git"},
		"strategy": map[string]any{"name": "kaniko", "kind": "ClusterBuildStrategy"},
	}
	if policy != nil {
		contract["policy"] = policy
	}
	b := &buildv1.Build{ObjectMeta: metav1.ObjectMeta{Name: testBuildName, Namespace: testNamespace, Labels: appLabels}}
	b.Spec.Contract = testsupport.RawContract(contract)
	return b
}

func newPushEvent() *eventsv1alpha1.GitHubEvent {
	e := &eventsv1alpha1.GitHubEvent{ObjectMeta: metav1.ObjectMeta{Name: "push-1", Namespace: "argo-events", Labels: appLabels}}
	e.Spec.Contract = testsupport.RawContract(map[string]any{
		"repository": "acme/app", "eventType": eventPush, "eventId": "delivery-1",
		"ref": testRef, "commitSHA": testSHA,
	})
	return e
}

func triggerSHA(t *testing.T, c client.Client) string {
	t.Helper()
	var b buildv1.Build
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testBuildName}, &b); err != nil {
		t.Fatalf("get build: %v", err)
	}
	return b.Annotations[triggerSHAAnnotation]
}

// TestReconcile_PolicyAndTriggers covers which Builds a matching push event
// annotates. A Build without a policy block must be left alone, not crash
// the observer.
func TestReconcile_PolicyAndTriggers(t *testing.T) {
	tests := []struct {
		name    string
		policy  map[string]any
		wantSHA string
	}{
		{name: "no policy block", policy: nil},
		{name: "empty policy", policy: map[string]any{}},
		{name: "retry without allowedTriggers", policy: map[string]any{"retry": map[string]any{"onFailure": true, "maxAttempts": 2}}},
		{name: "allowedTriggers without the event type", policy: map[string]any{keyTriggers: []any{map[string]any{"type": "pull_request"}}}},
		{name: "allowedTriggers with the event type", policy: map[string]any{keyTriggers: []any{map[string]any{"type": eventPush}}}, wantSHA: testSHA},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testsupport.NewFakeClient(newBuild(tt.policy), newPushEvent())
			r := &Reconciler{Client: c}

			req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: testNamespace, Name: testBuildName}}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if got := triggerSHA(t, c); got != tt.wantSHA {
				t.Errorf("trigger-sha = %q, want %q", got, tt.wantSHA)
			}
		})
	}
}

func pushPolicy() map[string]any {
	return map[string]any{keyTriggers: []any{map[string]any{"type": eventPush}}}
}

func reconcileBuild(t *testing.T, r *Reconciler) {
	t.Helper()
	req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: testNamespace, Name: testBuildName}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// setPayload overwrites the push payload on the one GitHubEvent CR, the way
// the Sensor does for every webhook delivery.
func setPayload(t *testing.T, c client.Client, eventID, sha string) {
	t.Helper()
	var e eventsv1alpha1.GitHubEvent
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "argo-events", Name: "push-1"}, &e); err != nil {
		t.Fatalf("get githubevent: %v", err)
	}
	e.Spec.Contract = testsupport.RawContract(map[string]any{
		"repository": "acme/app", "eventType": eventPush, "eventId": eventID,
		"ref": testRef, "commitSHA": sha,
	})
	if err := c.Update(context.Background(), &e); err != nil {
		t.Fatalf("update githubevent: %v", err)
	}
}

// TestReconcile_NewPushPayloadReplacesTheTrigger follows one GitHubEvent CR
// through three deliveries. The Build always ends up on the latest commit:
// the event holds the current state of the branch, not a queue of pushes.
func TestReconcile_NewPushPayloadReplacesTheTrigger(t *testing.T) {
	c := testsupport.NewFakeClient(newBuild(pushPolicy()), newPushEvent())
	r := &Reconciler{Client: c}

	reconcileBuild(t, r)
	if got := triggerSHA(t, c); got != testSHA {
		t.Fatalf("after the first push: trigger-sha = %q, want %q", got, testSHA)
	}

	// Reconciling again without a new delivery changes nothing.
	reconcileBuild(t, r)
	if got := triggerSHA(t, c); got != testSHA {
		t.Fatalf("after a repeat reconcile: trigger-sha = %q, want %q", got, testSHA)
	}

	// Two pushes land before the Build is reconciled; only the last is seen.
	setPayload(t, c, "delivery-2", "2222222bbbbbbb")
	setPayload(t, c, "delivery-3", "3333333ccccccc")
	reconcileBuild(t, r)
	if got := triggerSHA(t, c); got != "3333333ccccccc" {
		t.Fatalf("after two more pushes: trigger-sha = %q, want the latest", got)
	}

	var b buildv1.Build
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testBuildName}, &b); err != nil {
		t.Fatalf("get build: %v", err)
	}
	if b.Annotations[triggerTypeAnnotation] != eventPush || b.Annotations[triggerRefAnnotation] != testRef ||
		b.Annotations[triggerSourceAnnotation] != "github" {
		t.Errorf("trigger annotations = %v", b.Annotations)
	}
}

// TestReconcile_PayloadWithoutACommitKeepsTheTrigger covers a delivery that
// carries no commit: it must not blank out the commit the Build is on.
func TestReconcile_PayloadWithoutACommitKeepsTheTrigger(t *testing.T) {
	c := testsupport.NewFakeClient(newBuild(pushPolicy()), newPushEvent())
	r := &Reconciler{Client: c}
	reconcileBuild(t, r)

	setPayload(t, c, "delivery-2", "")
	reconcileBuild(t, r)

	if got := triggerSHA(t, c); got != testSHA {
		t.Errorf("trigger-sha = %q, want it to stay %q", got, testSHA)
	}
}

// TestMapGitHubEventToBuilds is the wake-up path: a changed GitHubEvent
// enqueues every Build of the same application, in any namespace.
func TestMapGitHubEventToBuilds(t *testing.T) {
	other := newBuild(pushPolicy())
	other.Name = "other-build"
	other.Namespace = "staging"

	unrelated := newBuild(pushPolicy())
	unrelated.Name = "unrelated-build"
	unrelated.Labels = map[string]string{query.LabelEnvironmentName: "another-app"}

	c := testsupport.NewFakeClient(newBuild(pushPolicy()), other, unrelated)
	r := &Reconciler{Client: c}

	got := map[string]bool{}
	for _, req := range r.mapGitHubEventToBuilds(context.Background(), newPushEvent()) {
		got[req.Namespace+"/"+req.Name] = true
	}
	if len(got) != 2 || !got[testNamespace+"/"+testBuildName] || !got["staging/other-build"] {
		t.Errorf("enqueued Builds = %v, want the two Builds of %s", got, testApp)
	}

	unlabelled := newPushEvent()
	unlabelled.Labels = nil
	if reqs := r.mapGitHubEventToBuilds(context.Background(), unlabelled); len(reqs) != 0 {
		t.Errorf("an event without the application label enqueued %v", reqs)
	}
}
