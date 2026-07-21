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

package environment

import (
	"context"
	"encoding/json"
	"testing"

	env1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	environmentResolution "github.com/blanketops/environments/resolution/environment/resolve"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

// Repeated across fixtures below — named to satisfy goconst.
const (
	testBuildName      = "build-sample"
	testNamespace      = "default"
	testAppSampleName  = "app-sample"
	keyApplicationName = "applicationName"
	testAppNewName     = "app-new"
)

func newScopedObject(envName, envType string) client.Object {
	return &env1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testBuildName,
			Namespace: testNamespace,
			Labels: map[string]string{
				"environments.blanketops.dev/name": envName,
				"environments.blanketops.dev/type": envType,
			},
		},
	}
}

func TestEnvironmentGone_TrueWhenDeleted(t *testing.T) {
	c := testsupport.NewFakeClient()

	gone, err := EnvironmentGone(context.Background(), c, testNamespace, map[string]string{
		LabelEnvironmentName: testAppSampleName,
	})
	if err != nil {
		t.Fatalf("EnvironmentGone() = %v, want nil error", err)
	}
	if !gone {
		t.Error("EnvironmentGone() = false, want true for a nonexistent Environment")
	}
}

func TestEnvironmentGone_FalseWhenPresent(t *testing.T) {
	existing := &env1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: testAppSampleName, Namespace: testNamespace},
		Spec:       env1alpha1.EnvironmentSpec{Contract: testsupport.RawContract(map[string]any{keyApplicationName: testAppSampleName})},
	}
	c := testsupport.NewFakeClient(existing)

	gone, err := EnvironmentGone(context.Background(), c, testNamespace, map[string]string{
		LabelEnvironmentName: testAppSampleName,
	})
	if err != nil {
		t.Fatalf("EnvironmentGone() = %v, want nil error", err)
	}
	if gone {
		t.Error("EnvironmentGone() = true, want false for an existing Environment")
	}
}

func TestEnvironmentGone_FalseWhenLabelMissing(t *testing.T) {
	c := testsupport.NewFakeClient()

	gone, err := EnvironmentGone(context.Background(), c, testNamespace, map[string]string{})
	if err != nil {
		t.Fatalf("EnvironmentGone() = %v, want nil error", err)
	}
	if gone {
		t.Error("EnvironmentGone() = true, want false when the name label is absent — that's query.Lookup's error to report, not this check's")
	}
}

func TestEnsureEnvironment_NotEnvironmentScoped(t *testing.T) {
	c := testsupport.NewFakeClient()
	obj := &env1alpha1.Build{ObjectMeta: metav1.ObjectMeta{Name: testBuildName, Namespace: testNamespace}}

	env, err := EnsureEnvironment(context.Background(), c, obj, runtime.RawExtension{})
	if err != nil {
		t.Fatalf("EnsureEnvironment() = %v, want nil", err)
	}
	if env != nil {
		t.Errorf("EnsureEnvironment() with no environment labels = %+v, want nil", env)
	}
}

func TestEnsureEnvironment_ReturnsExisting(t *testing.T) {
	existing := &env1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: testAppSampleName, Namespace: testNamespace},
		Spec:       env1alpha1.EnvironmentSpec{Contract: testsupport.RawContract(map[string]any{keyApplicationName: testAppSampleName})},
	}
	c := testsupport.NewFakeClient(existing)
	obj := newScopedObject(testAppSampleName, "dev")

	env, err := EnsureEnvironment(context.Background(), c, obj, runtime.RawExtension{})
	if err != nil {
		t.Fatalf("EnsureEnvironment() = %v, want nil", err)
	}
	if env == nil || env.Name != testAppSampleName {
		t.Errorf("EnsureEnvironment() = %+v, want the existing Environment returned as-is", env)
	}
}

func TestEnsureEnvironment_CreatesWhenMissing(t *testing.T) {
	c := testsupport.NewFakeClient()
	obj := newScopedObject(testAppNewName, "dev")
	contract := testsupport.RawContract(map[string]any{keyApplicationName: testAppNewName})

	env, err := EnsureEnvironment(context.Background(), c, obj, contract)
	if err != nil {
		t.Fatalf("EnsureEnvironment() = %v, want nil", err)
	}
	if env == nil {
		t.Fatal("EnsureEnvironment() = nil, want a newly created Environment")
	}
	if env.Labels["environments.blanketops.dev/name"] != testAppNewName || env.Labels["environments.blanketops.dev/type"] != "dev" {
		t.Errorf("created Environment labels = %v, want name=app-new type=dev", env.Labels)
	}

	// Verify it was actually persisted, not just returned in-memory.
	var fetched env1alpha1.Environment
	if err := c.Get(context.Background(), client.ObjectKey{Name: testAppNewName, Namespace: testNamespace}, &fetched); err != nil {
		t.Fatalf("created Environment not found in client: %v", err)
	}
}

func TestPatchEnvironmentAggregate_ResolutionFailure(t *testing.T) {
	c := testsupport.NewFakeClient()
	env := &env1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: testAppSampleName, Namespace: testNamespace},
		// No contract set — ResolveEnvironment requires spec.contract.
	}

	err := PatchEnvironmentAggregate(context.Background(), c, env, func(spec *environmentResolution.ResolvedEnvironmentSpec) {})
	if err == nil {
		t.Fatal("PatchEnvironmentAggregate() with empty contract = nil error, want error")
	}
}

func TestPatchEnvironmentAggregate_AppliesMutation(t *testing.T) {
	env := &env1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: testAppSampleName, Namespace: testNamespace},
		Spec: env1alpha1.EnvironmentSpec{
			Contract: testsupport.RawContract(map[string]any{
				keyApplicationName: testAppSampleName,
				"branch":           "main",
				"gitOwner":         "blanketops",
				"environmentType":  "dev",
				"version":          "v1",
			}),
		},
	}
	c := testsupport.NewFakeClient(env)

	err := PatchEnvironmentAggregate(context.Background(), c, env, func(spec *environmentResolution.ResolvedEnvironmentSpec) {
		spec.Build = testBuildName
	})
	if err != nil {
		t.Fatalf("PatchEnvironmentAggregate() = %v, want nil", err)
	}

	var patched map[string]any
	if err := json.Unmarshal(env.Spec.Contract.Raw, &patched); err != nil {
		t.Fatalf("failed to decode patched contract: %v", err)
	}
	if patched["Build"] != testBuildName {
		t.Errorf("patched contract Build = %v, want %q", patched["Build"], testBuildName)
	}

	// Also verify the update was actually persisted via the client, not
	// just mutated on the in-memory object passed in.
	var fetched env1alpha1.Environment
	if err := c.Get(context.Background(), client.ObjectKey{Name: testAppSampleName, Namespace: testNamespace}, &fetched); err != nil {
		t.Fatalf("get after patch: %v", err)
	}
	var fetchedContract map[string]any
	if err := json.Unmarshal(fetched.Spec.Contract.Raw, &fetchedContract); err != nil {
		t.Fatalf("failed to decode fetched contract: %v", err)
	}
	if fetchedContract["Build"] != testBuildName {
		t.Errorf("persisted contract Build = %v, want %q", fetchedContract["Build"], testBuildName)
	}
}
