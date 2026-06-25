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
	"fmt"

	env1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	environmentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/environment"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

func patchAndMergeRule(
	ctx context.Context,
	c client.Client,
	env *env1alpha1.Environment,
	mutate func(contract map[string]any),
) error {

	// 1. Take a deep copy for merge base
	original := env.DeepCopy()

	// 2. Decode existing contract (or initialize)
	var contract map[string]any
	if len(env.Spec.Contract.Raw) > 0 {
		if err := json.Unmarshal(env.Spec.Contract.Raw, &contract); err != nil {
			return fmt.Errorf("decode environment contract: %w", err)
		}
	} else {
		contract = make(map[string]any)
	}

	// 3. Apply ONLY this controller's mutation
	mutate(contract)

	// 4. Re-encode contract
	raw, err := json.Marshal(contract)
	if err != nil {
		return fmt.Errorf("encode environment contract: %w", err)
	}

	env.Spec.Contract.Raw = raw

	// 5. Strategic merge patch
	return c.Patch(ctx, env, client.MergeFrom(original))
}
