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

// Domain-level tests (internal/domains/githubevent) already exercise
// EnsurePrerequisites/CleanupPrerequisites end-to-end through Handle(),
// including the missing-Environment failure path. These tests focus on
// this mediator's own nil-guards and idempotency.
package githubevents

import (
	"context"
	"strings"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/blanketops/environments-api/api/events/v1alpha1"
	githubeventResolution "github.com/blanketops/environments/resolution/githubevent/resolve"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

func newResolvedGitHubEvent() *githubeventResolution.ResolvedGitHubEvent {
	gh := &eventsv1alpha1.GitHubEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "githubevent-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	return &githubeventResolution.ResolvedGitHubEvent{
		Event: gh,
		Spec: &githubeventResolution.ResolvedGitHubEventSpec{
			Repository: "blanketops/app",
			EventType:  "push",
			EventID:    "delivery-123",
			Webhook: githubeventResolution.ResolvedWebhook{
				SecretRef: githubeventResolution.ResolvedSecretRef{Name: "app-webhook-secret", Key: "secret"},
			},
		},
	}
}

func TestMediator_EnsurePrerequisites_NilResolved(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	if err := m.EnsurePrerequisites(context.Background(), nil); err == nil {
		t.Fatal("EnsurePrerequisites(nil) = nil error, want error")
	}
}

func TestMediator_EnsurePrerequisites_MissingEnvironment(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	if err := m.EnsurePrerequisites(context.Background(), newResolvedGitHubEvent()); err == nil {
		t.Fatal("EnsurePrerequisites() with no Environment = nil error, want error")
	}
}

func TestMediator_EnsurePrerequisites_Idempotent(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedGitHubEvent()
	c := testsupport.NewFakeClient(env, resolved.Event)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("first EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("second EnsurePrerequisites() = %v, want nil (must be idempotent)", err)
	}
}

func TestMediator_CleanupPrerequisites_NilResolved(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	if err := m.CleanupPrerequisites(context.Background(), nil); err == nil {
		t.Fatal("CleanupPrerequisites(nil) = nil error, want error")
	}
}

func TestMediator_CleanupPrerequisites_NothingProvisioned(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedGitHubEvent()
	c := testsupport.NewFakeClient(env, resolved.Event)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() on nothing provisioned = %v, want nil", err)
	}
}

func TestMediator_CleanupPrerequisites_AfterEnsure(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedGitHubEvent()
	c := testsupport.NewFakeClient(env, resolved.Event)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() = %v, want nil", err)
	}
}

// appsNamespace is a namespace other than the one GitHubEvents are delivered to.
const appsNamespace = "apps"

// newEnvironmentIn returns the test Environment in the given namespace.
func newEnvironmentIn(namespace string) *environmentsv1alpha1.Environment {
	env := newEnvironment()
	env.Namespace = namespace
	return env
}

// newResolvedGitHubEventIn returns the test GitHubEvent in the given
// namespace, as a webhook delivery lands in argo-events.
func newResolvedGitHubEventIn(namespace string) *githubeventResolution.ResolvedGitHubEvent {
	resolved := newResolvedGitHubEvent()
	resolved.Event.Namespace = namespace
	return resolved
}

func TestMediator_EnsurePrerequisites_EnvironmentInAnotherNamespace(t *testing.T) {
	resolved := newResolvedGitHubEventIn("argo-events")
	c := testsupport.NewFakeClient(newEnvironmentIn(appsNamespace), resolved.Event)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() = %v, want nil", err)
	}
}

func TestMediator_lookupEnvironment(t *testing.T) {
	tests := []struct {
		name          string
		envNamespaces []string
		eventLabels   map[string]string
		wantErr       string
	}{
		{
			name:          "environment in the event namespace",
			envNamespaces: []string{"argo-events"},
		},
		{
			name:          "environment in another namespace",
			envNamespaces: []string{appsNamespace},
		},
		{
			name:          "event namespace wins over others",
			envNamespaces: []string{appsNamespace, "argo-events", "staging"},
		},
		{
			name:          "several other namespaces is ambiguous",
			envNamespaces: []string{"staging", appsNamespace},
			wantErr:       `environment "app-sample" is ambiguous: found in namespaces apps, staging`,
		},
		{
			name:    "no environment anywhere",
			wantErr: "not found",
		},
		{
			name:          "event without the environment label",
			envNamespaces: []string{appsNamespace},
			eventLabels:   map[string]string{},
			wantErr:       "is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved := newResolvedGitHubEventIn("argo-events")
			if tt.eventLabels != nil {
				resolved.Event.Labels = tt.eventLabels
			}
			objs := []client.Object{resolved.Event}
			for _, ns := range tt.envNamespaces {
				objs = append(objs, newEnvironmentIn(ns))
			}
			m := New(testsupport.NewFakeClient(objs...), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

			envCtx, err := m.lookupEnvironment(context.Background(), resolved.Event)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("lookupEnvironment() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("lookupEnvironment() = %v, want nil", err)
			}
			if envCtx.Name != testAppName {
				t.Errorf("environment name = %q, want %q", envCtx.Name, testAppName)
			}
		})
	}
}
