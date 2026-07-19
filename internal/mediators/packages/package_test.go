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

// Domain-level tests (internal/domains/packages) already exercise
// EnsurePrerequisites/CleanupPrerequisites end-to-end through Handle().
// These tests focus on this mediator's own nil-guards, idempotency, and —
// most importantly — the StateRepository-nil regression: EnsurePrerequisites/
// CleanupPrerequisites previously dereferenced the documented-optional
// StateRepository pointer unconditionally, panicking for any Package that
// doesn't track state via GitOps. Fixed alongside these tests; the "no
// StateRepository" cases below lock that fix down.
package packages

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	packageResolution "github.com/blanketops/environments/resolution/packages/resolve"
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

func newResolvedPackage(stateRepo *packageResolution.ResolvedStateRepository, registryCredsSecret string) *packageResolution.ResolvedPackage {
	pkg := &environmentsv1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "package-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	return &packageResolution.ResolvedPackage{
		Package: pkg,
		Spec: &packageResolution.ResolvedPackageSpec{
			Name:    "app",
			Version: "1.0.0",
			PackageRepository: packageResolution.ResolvedPackageRepository{
				URL:               "oci://ghcr.io/blanketops/packages/app",
				CredentialsSecret: registryCredsSecret,
			},
			StateRepository: stateRepo,
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
	resolved := newResolvedPackage(nil, "")
	if err := m.EnsurePrerequisites(context.Background(), resolved); err == nil {
		t.Fatal("EnsurePrerequisites() with no Environment = nil error, want error")
	}
}

// Regression test for the StateRepository-nil-pointer bug: a Package with
// no stateRepo declared (StateRepository == nil) and no registry
// credentials secret must provision cleanly — previously panicked.
func TestMediator_EnsurePrerequisites_NoStateRepository_NoPanic(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedPackage(nil, "")
	c := testsupport.NewFakeClient(env, resolved.Package)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() with nil StateRepository = %v, want nil", err)
	}
}

func TestMediator_EnsurePrerequisites_WithStateRepository(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedPackage(&packageResolution.ResolvedStateRepository{
		URL:         "https://github.com/blanketops/app-state.git",
		CloneSecret: "app-state-git-ssh",
	}, "")
	c := testsupport.NewFakeClient(env, resolved.Package)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() with StateRepository = %v, want nil", err)
	}
}

func TestMediator_EnsurePrerequisites_Idempotent(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedPackage(&packageResolution.ResolvedStateRepository{
		URL:         "https://github.com/blanketops/app-state.git",
		CloneSecret: "app-state-git-ssh",
	}, "app-registry-creds")
	c := testsupport.NewFakeClient(env, resolved.Package)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("first EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("second EnsurePrerequisites() = %v, want nil (must be idempotent)", err)
	}
}

// Regression test mirroring the Ensure-side fix, for CleanupPrerequisites.
func TestMediator_CleanupPrerequisites_NoStateRepository_NoPanic(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedPackage(nil, "")
	c := testsupport.NewFakeClient(env, resolved.Package)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() with nil StateRepository = %v, want nil", err)
	}
}

func TestMediator_CleanupPrerequisites_AfterEnsure(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedPackage(&packageResolution.ResolvedStateRepository{
		URL:         "https://github.com/blanketops/app-state.git",
		CloneSecret: "app-state-git-ssh",
	}, "app-registry-creds")
	c := testsupport.NewFakeClient(env, resolved.Package)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() = %v, want nil", err)
	}
}
