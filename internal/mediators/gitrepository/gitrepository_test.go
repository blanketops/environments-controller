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

// Domain-level tests (internal/domains/gitrepository) already exercise
// EnsurePrerequisites/CleanupPrerequisites end-to-end through Handle(),
// including the missing-Environment failure path. These tests focus on
// this mediator's own nil-guards, idempotency, and — most importantly —
// the deliberate teardown asymmetry documented in the package doc: shared
// cluster-level prerequisites (provider credentials, ProviderConfig) must
// survive a single GitRepository's cleanup.
package gitrepository

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	sourcesv1alpha1 "github.com/blanketops/environments-api/api/sources/v1alpha1"
	gitrepoResolution "github.com/blanketops/environments/resolution/gitrepository/resolve"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func newResolvedGitRepository() *gitrepoResolution.ResolvedGitRepository {
	repo := &sourcesv1alpha1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitrepo-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	return &gitrepoResolution.ResolvedGitRepository{
		Repository: repo,
		Spec: &gitrepoResolution.ResolvedGitRepositorySpec{
			Provider: "github",
			HookURL:  "https://events.blanketops.dev/hooks/app-sample",
			Repository: gitrepoResolution.GitRepositoryRef{
				Owner: "blanketops",
				Name:  "app",
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
	if err := m.EnsurePrerequisites(context.Background(), newResolvedGitRepository()); err == nil {
		t.Fatal("EnsurePrerequisites() with no Environment = nil error, want error")
	}
}

func TestMediator_EnsurePrerequisites_Idempotent(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedGitRepository()
	c := testsupport.NewFakeClient(env, resolved.Repository)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("first EnsurePrerequisites() = %v, want nil", err)
	}
	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("second EnsurePrerequisites() = %v, want nil (must be idempotent)", err)
	}
}

func TestMediator_CleanupPrerequisites_PreservesSharedPrerequisites(t *testing.T) {
	env := newEnvironment()
	resolved := newResolvedGitRepository()
	c := testsupport.NewFakeClient(env, resolved.Repository)
	m := New(c, testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if err := m.EnsurePrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("EnsurePrerequisites() = %v, want nil", err)
	}

	// The shared, cluster-level ProviderConfig (name "github-upjet", cluster-
	// scoped) must exist after provisioning.
	var pc unstructured.Unstructured
	pc.SetGroupVersionKind(schema.GroupVersionKind{Group: "github.upbound.io", Version: "v1beta1", Kind: "ProviderConfig"})
	if err := c.Get(context.Background(), client.ObjectKey{Name: "github-upjet"}, &pc); err != nil {
		t.Fatalf("expected ProviderConfig to exist after EnsurePrerequisites, got: %v", err)
	}

	if err := m.CleanupPrerequisites(context.Background(), resolved); err != nil {
		t.Fatalf("CleanupPrerequisites() = %v, want nil", err)
	}

	// Per the package doc: CleanupPrerequisites deliberately never tears
	// down the shared ProviderConfig — deleting it on a single
	// GitRepository's teardown would sever provider access for every other
	// GitRepository in the cluster.
	if err := c.Get(context.Background(), client.ObjectKey{Name: "github-upjet"}, &pc); err != nil {
		t.Errorf("ProviderConfig was removed by CleanupPrerequisites, want it preserved (shared prerequisite): %v", err)
	}
}
