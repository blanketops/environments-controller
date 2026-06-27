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

package deployment

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/pkg/environment/query"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/git"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder events.EventRecorder
	// DeploymentFluxGitSSHSecretReconciler has no store dependency — generates keypair locally.
	DeploymentFluxGitSSHSecretReconciler *git.DeploymentFluxGitSSHSecretReconciler
}

func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, recorder events.EventRecorder) *Mediator {
	return &Mediator{
		Client:                               c,
		Scheme:                               scheme,
		Log:                                  log,
		Recorder:                             recorder,
		DeploymentFluxGitSSHSecretReconciler: git.NewDeploymentFluxGitSSHSecretReconciler(c, log),
	}
}

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) error {
	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedDeployment (resolver bug)")
	}

	deploy := resolved.Deployment

	log := m.Log.WithValues("deployment", deploy.Name, "namespace", deploy.Namespace)
	log.Info("ensuring deployment prerequisites")

	// ── Step 0: Environment lookup ────────────────────────────────────────────
	envCtx, err := query.Lookup(ctx, m.Client, deploy.Namespace, deploy.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}

	log.Info("environment context resolved",
		"environment", envCtx.Name,
		"type", envCtx.EnvironmentType,
		"store", envCtx.StoreName,
	)

	// ── Step 1: Git SSH secret (store-dependent) ──────────────────────────────
	gitSSH := git.NewDeploymentGitSSHSecretReconciler(m.Client, m.Log, envCtx.StoreName)
	if err := gitSSH.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile git ssh secret: %w", err)
	}

	// ── Step 2: Flux SSH secret (no store — keypair generated locally) ────────
	if err := m.DeploymentFluxGitSSHSecretReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile fluxcd git ssh secret: %w", err)
	}

	// ── Step 3: GitOps manifests repo ─────────────────────────────────────────
	if resolved.Spec.ManifestsRepo != nil {
		if err := m.ensureManifestsRepo(ctx, resolved); err != nil {
			return err
		}
	}

	// ── Step 4: Runtime infra ─────────────────────────────────────────────────
	if err := m.ensureRuntime(ctx, resolved); err != nil {
		return err
	}

	log.Info("all deployment prerequisites satisfied")
	return nil
}

func (m *Mediator) ensureRuntime(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) error {
	return nil
}

// func (m *Mediator) ensureKubernetesRuntime(
// 	ctx context.Context,
// 	namespace string,
// ) error {
// 	m.Log.Info("ensuring Kubernetes runtime", "namespace", namespace)
// 	ns := &corev1.Namespace{}
// 	if err := m.Client.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
// 		return fmt.Errorf("kubernetes namespace %q not available: %w", namespace, err)
// 	}
// 	return nil
// }

// func toRawContract(spec *environmentResolution.ResolvedEnvironmentSpec) (runtime.RawExtension, error) {
// 	contract := spec.ToEnvironmentContract()
// 	raw, err := json.Marshal(contract)
// 	if err != nil {
// 		return runtime.RawExtension{}, err
// 	}
// 	return runtime.RawExtension{Raw: raw}, nil
// }

// func (m *Mediator) ensureAndPatchEnvironment(
// 	ctx context.Context,
// 	resolved *deploymentResolution.ResolvedDeployment,
// ) error {
// 	deploy := resolved.Deployment
// 	labels := deploy.GetLabels()

// 	envName := labels["environments.blanketops.dev/name"]
// 	envType := labels["environments.blanketops.dev/type"]
// 	if envName == "" || envType == "" {
// 		return nil
// 	}

// 	key := client.ObjectKey{Name: envName, Namespace: deploy.Namespace}
// 	var env env1alpha1.Environment
// 	err := m.Client.Get(ctx, key, &env)

// 	if apierrors.IsNotFound(err) {
// 		spec := &environmentResolution.ResolvedEnvironmentSpec{
// 			ApplicationName: envName,
// 			EnvironmentType: envType,
// 			Deployment:      deploy.Name,
// 		}
// 		raw, err := toRawContract(spec)
// 		if err != nil {
// 			return err
// 		}
// 		env = env1alpha1.Environment{
// 			ObjectMeta: metav1.ObjectMeta{
// 				Name:      envName,
// 				Namespace: deploy.Namespace,
// 				Labels: map[string]string{
// 					"environments.blanketops.dev/name": envName,
// 					"environments.blanketops.dev/type": envType,
// 				},
// 			},
// 			Spec: env1alpha1.EnvironmentSpec{Contract: raw},
// 		}
// 		return m.Client.Create(ctx, &env)
// 	}
// 	if err != nil {
// 		return err
// 	}

// 	resolvedEnv, err := environmentResolution.ResolveEnvironment(&env)
// 	if err != nil {
// 		return err
// 	}
// 	if resolvedEnv.Spec.Deployment == deploy.Name {
// 		return nil
// 	}
// 	resolvedEnv.Spec.Deployment = deploy.Name
// 	raw, err := toRawContract(resolvedEnv.Spec)
// 	if err != nil {
// 		return err
// 	}
// 	env.Spec.Contract = raw
// 	return m.Client.Update(ctx, &env)
// }
