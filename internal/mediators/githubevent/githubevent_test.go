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
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/blanketops/environments-api/api/events/v1alpha1"
	githubeventResolution "github.com/blanketops/environments/resolution/githubevent/resolve"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// TestMediator_CleanupPrerequisites_NoEnvironment locks in the fact that
// teardown does not depend on the Environment lookup. In production,
// GitHubEvent CRs are written by the Argo Events Sensor into the fixed
// argo-events namespace — never the Environment's own namespace — so a
// lookup gated the same way as EnsurePrerequisites would make teardown
// permanently unsatisfiable. No Environment exists in the fake client at
// all here, which would fail EnsurePrerequisites; CleanupPrerequisites must
// still succeed.
func TestMediator_CleanupPrerequisites_NoEnvironment(t *testing.T) {
	resolved := newResolvedGitHubEvent()
	c := testsupport.NewFakeClient(resolved.Event)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() with no Environment = %v, want nil", err)
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
