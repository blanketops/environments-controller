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

package githubevent

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/blanketops/environments-api/api/events/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	githubeventapi "github.com/blanketops/environments/pkg/apis/githubevent/api"
	"github.com/blanketops/environments/pkg/apis/githubevent/application"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	githubeventmediator "github.com/blanketops/environments-controller/internal/mediators/githubevent"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const testAppName = "app-sample"

func newEnvironment() *environmentsv1alpha1.Environment {
	return &environmentsv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAppName,
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
				"environments.blanketops.dev/type": "dev",
			},
		},
		Spec: environmentsv1alpha1.EnvironmentSpec{
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

func newGitHubEventCR(contract map[string]any) *eventsv1alpha1.GitHubEvent {
	gh := &eventsv1alpha1.GitHubEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "githubevent-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	if contract != nil {
		gh.Spec.Contract = testsupport.RawContract(contract)
	}
	return gh
}

func validGitHubEventContract() map[string]any {
	return map[string]any{
		"repository": "blanketops/app",
		"eventType":  "push",
		"eventId":    "delivery-123",
		"webhook": map[string]any{
			"secretRef": map[string]any{
				"name": "app-webhook-secret",
				"key":  "secret",
			},
		},
	}
}

func newTestDomain(t *testing.T, objs ...client.Object) *GitHubEventDomain {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()
	rawRec := testsupport.NoopRawRecorder()

	med := githubeventmediator.New(c, testsupport.NewScheme(), log, rawRec)

	mapper := application.NewMapper()
	statusWriter := application.NewStatusWriter(c, log)
	backend := application.NewBackendSelector(githubeventapi.NewGitHubProvider(c, testsupport.NewScheme(), log, rawRec))
	service := application.NewGitHubEventService(mapper, statusWriter, backend)

	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	return New(service, med, testsupport.NoopRecorder(), cache, log)
}

func conditionStatus(conds []metav1.Condition, condType string) (metav1.ConditionStatus, bool) {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status, true
		}
	}
	return "", false
}

func TestGitHubEventDomain_GVK(t *testing.T) {
	d := &GitHubEventDomain{}
	if gvk := d.GVK(); gvk.Kind != "GitHubEvent" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "GitHubEvent")
	}
}

func TestGitHubEventDomain_CanCreate(t *testing.T) {
	d := &GitHubEventDomain{}
	if !d.CanCreate(&eventsv1alpha1.GitHubEvent{}) {
		t.Error("CanCreate(*GitHubEvent) = false, want true")
	}
	if d.CanCreate(&environmentsv1alpha1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestGitHubEventDomain_CanDelete_AlwaysFalse(t *testing.T) {
	// Events are historical facts — CanDelete always returns false,
	// regardless of object type, unlike every other domain's CanDelete.
	d := &GitHubEventDomain{}
	if d.CanDelete(&eventsv1alpha1.GitHubEvent{}) {
		t.Error("CanDelete(*GitHubEvent) = true, want false (events are never delete-reconciled)")
	}
}

func TestGitHubEventDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newGitHubEventCR(map[string]any{"repository": "a", "eventType": "push"}),
			newObj: newGitHubEventCR(map[string]any{"repository": "b", "eventType": "push"}),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newGitHubEventCR(validGitHubEventContract()),
			newObj: newGitHubEventCR(validGitHubEventContract()),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentsv1alpha1.Build{},
			newObj: newGitHubEventCR(validGitHubEventContract()),
			want:   false,
		},
	}

	d := &GitHubEventDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitHubEventDomain_Handle_InvalidObject(t *testing.T) {
	d := &GitHubEventDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentsv1alpha1.Build{}}); err == nil {
		t.Fatal("Handle() with non-GitHubEvent object = nil error, want error")
	}
}

func TestGitHubEventDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	gh := newGitHubEventCR(nil)
	d := newTestDomain(t, gh)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: gh})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(gh.Status.Conditions, "GitHubEventResolveFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("GitHubEventResolveFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestGitHubEventDomain_Handle_Create_MissingEnvironment(t *testing.T) {
	gh := newGitHubEventCR(validGitHubEventContract())
	d := newTestDomain(t, gh)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: gh})
	if err == nil {
		t.Fatal("Handle() with no owning Environment = nil error, want error")
	}
	if status, ok := conditionStatus(gh.Status.Conditions, "GitHubEventPrerequisitesCreateFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("GitHubEventPrerequisitesCreateFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestGitHubEventDomain_Handle_Create_Succeeds(t *testing.T) {
	env := newEnvironment()
	gh := newGitHubEventCR(validGitHubEventContract())
	d := newTestDomain(t, env, gh)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: gh}); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}

	for _, condType := range []string{"GitHubEventResolved", "GitHubEventCached", "GitHubEventPrerequisitesCreated", "GitHubEventOrganized"} {
		if status, ok := conditionStatus(gh.Status.Conditions, condType); !ok || status != metav1.ConditionTrue {
			t.Errorf("%s condition = (%v, found=%v), want (True, true)", condType, status, ok)
		}
	}
}

func TestGitHubEventDomain_Handle_Delete_ResolutionFailure(t *testing.T) {
	gh := newGitHubEventCR(nil)
	d := newTestDomain(t, gh)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: gh})
	if err == nil {
		t.Fatal("Handle() delete with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(gh.Status.Conditions, "GitHubEventDeleted"); !ok || status != metav1.ConditionFalse {
		t.Errorf("GitHubEventDeleted condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestGitHubEventDomain_Handle_Delete_Succeeds(t *testing.T) {
	env := newEnvironment()
	gh := newGitHubEventCR(validGitHubEventContract())
	d := newTestDomain(t, env, gh)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: gh}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}
	if status, ok := conditionStatus(gh.Status.Conditions, "GitHubEventDeleted"); !ok || status != metav1.ConditionTrue {
		t.Errorf("GitHubEventDeleted condition = (%v, found=%v), want (True, true)", status, ok)
	}
}
