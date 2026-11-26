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

// manifests_repo.go's ensureDeployKey/ensureGitHubRepoPrivate/deleteGitHubRepo
// call the real GitHub API (hardcoded https://api.github.com, no injectable
// client/base URL) and shell out to the git CLI — not unit-testable without
// a production-code refactor to inject an HTTP client, which is out of
// scope for adding coverage. These tests cover the pure/fake-client-only
// paths: the nil/no-ManifestsRepo guards, and the two Secret-reading
// helpers that only touch the fake client and local filesystem.
package deployment

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	deploymentResolution "github.com/blanketops/environments/resolution/deployment/resolve"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

// Repeated across fixtures below — named to satisfy goconst.
const (
	testNamespace    = "default"
	manifestsRepoURL = "https://github.com/blanketops/app-manifests.git"
)

func newResolvedDeployment(manifestsRepo *deploymentResolution.ResolvedManifestsRepo) *deploymentResolution.ResolvedDeployment {
	depl := &environmentsv1alpha1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deployment-sample",
			Namespace: testNamespace,
		},
	}
	return &deploymentResolution.ResolvedDeployment{
		Deployment: depl,
		Spec: &deploymentResolution.ResolvedDeploymentSpec{
			ManifestsRepo: manifestsRepo,
			GitOwner:      "blanketops",
		},
	}
}

func TestEnsureManifestsRepo_NilResolved(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	if err := m.ensureManifestsRepo(context.Background(), nil); err == nil {
		t.Fatal("ensureManifestsRepo(nil) = nil error, want error")
	}
}

func TestEnsureManifestsRepo_NoManifestsRepo_NoOp(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	resolved := newResolvedDeployment(nil)
	// No ManifestsRepo declared — must return nil without attempting any
	// GitHub API call or git operation.
	if err := m.ensureManifestsRepo(context.Background(), resolved); err != nil {
		t.Fatalf("ensureManifestsRepo() with no ManifestsRepo = %v, want nil", err)
	}
}

func TestTeardownManifestsRepo_NilResolved(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	if err := m.teardownManifestsRepo(nil); err == nil {
		t.Fatal("teardownManifestsRepo(nil) = nil error, want error")
	}
}

func TestTeardownManifestsRepo_NoManifestsRepo_NoOp(t *testing.T) {
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())
	resolved := newResolvedDeployment(nil)
	if err := m.teardownManifestsRepo(resolved); err != nil {
		t.Fatalf("teardownManifestsRepo() with no ManifestsRepo = %v, want nil", err)
	}
}

func TestExtractPublicKey_PreStoredIdentityPub(t *testing.T) {
	resolved := newResolvedDeployment(&deploymentResolution.ResolvedManifestsRepo{URL: manifestsRepoURL})
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deployment-sample-flux-ssh",
			Namespace: testNamespace,
		},
		Data: map[string][]byte{
			"identity.pub": []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINPreStoredKey test@blanketops"),
		},
	}
	m := New(testsupport.NewFakeClient(secret), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	pub, err := m.extractPublicKey(context.Background(), resolved)
	if err != nil {
		t.Fatalf("extractPublicKey() = %v, want nil", err)
	}
	if pub != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINPreStoredKey test@blanketops" {
		t.Errorf("extractPublicKey() = %q, want the pre-stored identity.pub verbatim", pub)
	}
}

func TestExtractPublicKey_SecretNotFound(t *testing.T) {
	resolved := newResolvedDeployment(&deploymentResolution.ResolvedManifestsRepo{URL: manifestsRepoURL})
	m := New(testsupport.NewFakeClient(), testsupport.NewScheme(), logr.Discard(), testsupport.NoopRawRecorder())

	if _, err := m.extractPublicKey(context.Background(), resolved); err == nil {
		t.Fatal("extractPublicKey() with no flux-ssh secret = nil error, want error")
	}
}
