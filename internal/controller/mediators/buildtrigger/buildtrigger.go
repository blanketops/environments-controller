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

package buildtrigger

import (
	"context"
	"encoding/json"

	"github.com/go-logr/logr"
	env1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	buildtriggerResolution "github.com/ntlaletsi70/blanketops-environments/resolution/buildtrigger"
	environmentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/environment"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder events.EventRecorder
}

func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	recorder events.EventRecorder,
) *Mediator {
	return &Mediator{
		Client:   c,
		Scheme:   scheme,
		Log:      log,
		Recorder: recorder,
	}
}

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *buildtriggerResolution.ResolvedBuildTrigger,
) error {

	if resolved == nil || resolved.Spec == nil {
		panic("nil ResolvedBuildTrigger passed to mediator")
	}

	trigger := resolved.Trigger
	labels := trigger.GetLabels()

	envName := labels["environments.blanketops.dev/name"]
	envType := labels["environments.blanketops.dev/type"]

	if envName == "" || envType == "" {
		// Not environment-scoped
		return nil
	}

	key := client.ObjectKey{
		Name:      envName,
		Namespace: trigger.Namespace,
	}

	var env env1alpha1.Environment
	err := m.Client.Get(ctx, key, &env)

	// -------------------------------------------------
	// CREATE Environment (empty shell)
	// -------------------------------------------------
	if apierrors.IsNotFound(err) {

		spec := &environmentResolution.ResolvedEnvironmentSpec{
			ApplicationName: envName,
			EnvironmentType: envType,
			BuildTriggers:   []string{trigger.Name},
		}

		raw, err := toRawContract(spec)
		if err != nil {
			return err
		}

		env = env1alpha1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      envName,
				Namespace: trigger.Namespace,
				Labels: map[string]string{
					"environments.blanketops.dev/name": envName,
					"environments.blanketops.dev/type": envType,
				},
			},
			Spec: env1alpha1.EnvironmentSpec{
				Contract: raw,
			},
		}

		m.Log.Info("creating environment shell (from buildtrigger)",
			"environment", envName,
			"trigger", trigger.Name,
		)

		return m.Client.Create(ctx, &env)
	}

	if err != nil {
		return err
	}

	// -------------------------------------------------
	// PATCH Environment aggregate (append trigger)
	// -------------------------------------------------
	resolvedEnv, err := environmentResolution.ResolveEnvironment(&env)
	if err != nil {
		return err
	}

	// idempotent append
	for _, name := range resolvedEnv.Spec.BuildTriggers {
		if name == trigger.Name {
			return nil
		}
	}

	resolvedEnv.Spec.BuildTriggers = append(
		resolvedEnv.Spec.BuildTriggers,
		trigger.Name,
	)

	raw, err := toRawContract(resolvedEnv.Spec)
	if err != nil {
		return err
	}

	env.Spec.Contract = raw

	m.Log.Info("patching environment aggregate (buildtrigger)",
		"environment", envName,
		"trigger", trigger.Name,
	)

	return m.Client.Update(ctx, &env)
}

func toRawContract(
	spec *environmentResolution.ResolvedEnvironmentSpec,
) (runtime.RawExtension, error) {

	contract := spec.ToEnvironmentContract()

	raw, err := json.Marshal(contract)
	if err != nil {
		return runtime.RawExtension{}, err
	}

	return runtime.RawExtension{Raw: raw}, nil
}
