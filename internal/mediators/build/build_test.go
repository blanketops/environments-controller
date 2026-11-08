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

// Domain-level tests (internal/domains/build) already exercise
// EnsurePrerequisites/CleanupPrerequisites end-to-end through Handle(),
// including the missing-Environment failure path. These tests focus on
// behaviors specific to this mediator that the domain tests don't already
// cover: idempotent re-provisioning and cleanup-when-nothing-exists.
package build

import (
	"context"
	"errors"
	"strings"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	buildResolution "github.com/blanketops/environments/resolution/build/resolve"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/blanketops/environments-controller/internal/testsupport"
	corev1 "k8s.io/api/core/v1"
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

func newResolvedBuild() *buildResolution.ResolvedBuild {
	build := &environmentsv1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	return &buildResolution.ResolvedBuild{
		Build: build,
		Spec: &buildResolution.ResolvedBuildSpec{
			Image: "ghcr.io/blanketops/app:latest",
			Source: buildResolution.ResolvedSource{
				URL:         "https://github.com/blanketops/app.git",
				CloneSecret: "app-git-ssh",
			},
			Strategy: buildResolution.ResolvedStrategy{Name: "kaniko", StrategyKind: "ClusterBuildStrategy"},
		},
	}
}

// cloneSecret is the Secret External Secrets writes for the Build's source.
// The fake client runs no controllers, so a test that needs the prerequisites
// to be in place creates it.
func cloneSecret() *corev1.Secret {
	b := newResolvedBuild()
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: b.Spec.Source.CloneSecret, Namespace: b.Build.Namespace}}
}

func TestMediator_EnsurePrerequisites_MissingEnvironment(t *testing.T) {
	c := testsupport.NewFakeClient()
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), newResolvedBuild()); err == nil {
		t.Fatal("EnsurePrerequisites() with no Environment = nil error, want error")
	}
}

func TestMediator_EnsurePrerequisites_Idempotent(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedBuild()
	c := testsupport.NewFakeClient(env, resolved.Build, cloneSecret())
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("first EnsurePrerequisites() = %v, want nil", err)
	}
	// Second call must be a clean no-op (Update, not Create-on-existing) —
	// the secret/SA reconcilers are Get-or-Create/Update, so calling this
	// twice in a row (e.g. two reconcile passes before the Build's
	// generation changes) must not error.
	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("second EnsurePrerequisites() = %v, want nil (must be idempotent)", err)
	}
}

func TestMediator_CleanupPrerequisites_NothingProvisioned(t *testing.T) {
	// Cleanup must succeed even when EnsurePrerequisites was never called —
	// e.g. a Build deleted before its first successful reconcile. The
	// underlying Delete() calls guard on apierrors.IsNotFound; this test
	// locks that guard down at the mediator level.
	env := newEnvironment()
	resolved := newResolvedBuild()
	c := testsupport.NewFakeClient(env, resolved.Build)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() on nothing provisioned = %v, want nil", err)
	}
}

func TestMediator_CleanupPrerequisites_AfterEnsure(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedBuild()
	c := testsupport.NewFakeClient(env, resolved.Build, cloneSecret())
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() = %v, want nil", err)
	}
}

// The ExternalSecrets are declared at once but the Secrets arrive later.
// Until every Secret the Build references exists the prerequisites are
// pending, naming what is missing, so nothing is dispatched: Shipwright
// fails every BuildRun of a Build created before its secrets.
func TestMediator_EnsurePrerequisites_PendingUntilTheSecretsExist(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedBuild()
	resolved.Spec.ServiceAccount = &buildResolution.ResolvedServiceAccount{Name: "build-bot", Secret: "registry-credentials"}
	c := testsupport.NewFakeClient(env, resolved.Build)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	ctx := context.Background()

	err := m.EnsurePrerequisites(ctx, resolved)
	if !errors.Is(err, ErrPrerequisitesPending) || !strings.Contains(err.Error(), "app-git-ssh") || !strings.Contains(err.Error(), "registry-credentials") {
		t.Fatalf("EnsurePrerequisites() = %v, want pending naming both secrets", err)
	}

	// One arrives.
	if err := c.Create(ctx, cloneSecret()); err != nil {
		t.Fatalf("create clone secret: %v", err)
	}
	err = m.EnsurePrerequisites(ctx, resolved)
	if !errors.Is(err, ErrPrerequisitesPending) || strings.Contains(err.Error(), "app-git-ssh") || !strings.Contains(err.Error(), "registry-credentials") {
		t.Fatalf("EnsurePrerequisites() = %v, want pending naming only the registry secret", err)
	}

	// Both are there.
	registry := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry-credentials", Namespace: resolved.Build.Namespace}}
	if err := c.Create(ctx, registry); err != nil {
		t.Fatalf("create registry secret: %v", err)
	}
	if err := m.EnsurePrerequisites(ctx, resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() with both secrets = %v, want nil", err)
	}
}
