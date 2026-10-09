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
		"repository": "acme/app", "eventType": "push", "eventId": "delivery-1",
		"ref": "refs/heads/main", "commitSHA": testSHA,
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
		{name: "allowedTriggers without the event type", policy: map[string]any{"allowedTriggers": []any{map[string]any{"type": "pull_request"}}}},
		{name: "allowedTriggers with the event type", policy: map[string]any{"allowedTriggers": []any{map[string]any{"type": "push"}}}, wantSHA: testSHA},
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
