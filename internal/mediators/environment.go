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

	env1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	environmentResolution "github.com/blanketops/environments/resolution/environment/resolve"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LabelEnvironmentName mirrors query.LabelEnvironmentName. Duplicated here
// rather than imported to keep this package free of the engine module's
// query package — EnvironmentGone is a pre-check callers run before invoking
// query.Lookup, not a replacement for it.
const LabelEnvironmentName = "environments.blanketops.dev/name"

// EnvironmentGone reports whether the Environment CR named by the
// environments.blanketops.dev/name label no longer exists. Mediators call
// this at the top of CleanupPrerequisites before query.Lookup: if the
// Environment was deleted out of order (ahead of, or alongside, its
// children), query.Lookup fails permanently — "must pre-exist" — which
// would keep the child's finalizer in place forever with no way to clear
// it short of a manual patch. When the Environment is confirmed gone, the
// store binding it authorized is no longer resolvable, so store-bound
// cleanup is skipped and the caller lets the finalizer proceed.
//
// A missing name label returns false (not gone) — that's a distinct
// misconfiguration query.Lookup already reports clearly, not something
// this check should paper over.
func EnvironmentGone(ctx context.Context, c client.Client, namespace string, labels map[string]string) (bool, error) {
	name := labels[LabelEnvironmentName]
	if name == "" {
		return false, nil
	}
	var env env1alpha1.Environment
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &env)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func EnsureEnvironment(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	contract runtime.RawExtension,
) (*env1alpha1.Environment, error) {

	envName := obj.GetLabels()["environments.blanketops.dev/name"]
	envType := obj.GetLabels()["environments.blanketops.dev/type"]

	if envName == "" || envType == "" {
		return nil, nil // not environment-scoped
	}

	key := client.ObjectKey{
		Name:      envName,
		Namespace: obj.GetNamespace(),
	}

	var env env1alpha1.Environment
	if err := c.Get(ctx, key, &env); err == nil {
		return &env, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}

	env = env1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envName,
			Namespace: obj.GetNamespace(),
			Labels: map[string]string{
				"environments.blanketops.dev/name": envName,
				"environments.blanketops.dev/type": envType,
			},
		},
		Spec: env1alpha1.EnvironmentSpec{
			Contract: contract,
		},
	}

	if err := c.Create(ctx, &env); err != nil {
		return nil, err
	}

	return &env, nil
}

func PatchEnvironmentAggregate(
	ctx context.Context,
	c client.Client,
	env *env1alpha1.Environment,
	patchFn func(*environmentResolution.ResolvedEnvironmentSpec),
) error {

	resolved, err := environmentResolution.ResolveEnvironment(env)
	if err != nil {
		return err
	}

	// Apply mutation
	patchFn(resolved.Spec)

	raw, err := json.Marshal(resolved.Spec)
	if err != nil {
		return err
	}

	env.Spec.Contract = runtime.RawExtension{
		Raw: raw,
	}

	return c.Update(ctx, env)
}

// func patchAndMergeRule(
// 	ctx context.Context,
// 	c client.Client,
// 	env *env1alpha1.Environment,
// 	mutate func(contract map[string]any),
// ) error {

// 	// 1. Take a deep copy for merge base
// 	original := env.DeepCopy()

// 	// 2. Decode existing contract (or initialize)
// 	var contract map[string]any
// 	if len(env.Spec.Contract.Raw) > 0 {
// 		if err := json.Unmarshal(env.Spec.Contract.Raw, &contract); err != nil {
// 			return fmt.Errorf("decode environment contract: %w", err)
// 		}
// 	} else {
// 		contract = make(map[string]any)
// 	}

// 	// 3. Apply ONLY this controller's mutation
// 	mutate(contract)

// 	// 4. Re-encode contract
// 	raw, err := json.Marshal(contract)
// 	if err != nil {
// 		return fmt.Errorf("encode environment contract: %w", err)
// 	}

// 	env.Spec.Contract.Raw = raw

// 	// 5. Strategic merge patch
// 	return c.Patch(ctx, env, client.MergeFrom(original))
// }
