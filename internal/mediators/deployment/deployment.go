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
/*
Package deployment implements the Deployment prerequisite mediator.
The mediator owns the cross-cutting prerequisites a Deployment requires
before the application layer may act: the git SSH secret, the Flux git SSH
keypair, the GitOps manifests repository, and the runtime infrastructure.
It is invoked by the Deployment domain during command handling — after
resolution, before execution — and again during teardown.
Prerequisite provisioning is gated on the Environment: the Environment CR
must pre-exist as the root of the delivery chain, and it is the sole
authority for the ClusterSecretStore binding used by every store-dependent
secret this mediator reconciles. The Flux keypair is the exception — it is
generated locally and has no store dependency.
*/
package deployment

import (
	"context"
	"fmt"

	"github.com/blanketops/environments/pkg/apis/environment/query"
	gitDeployment "github.com/blanketops/environments/pkg/secrets/git/deployment"
	gitFluxcd "github.com/blanketops/environments/pkg/secrets/git/fluxcd"
	deploymentResolution "github.com/blanketops/environments/resolution/deployment/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Mediator manages the prerequisite resources a Deployment depends on.
type Mediator struct {
	// Client is the Kubernetes client used for all prerequisite operations.
	Client client.Client

	// Scheme is the runtime scheme used for owner reference wiring.
	Scheme *runtime.Scheme

	// Log is the logger instance for this mediator.
	Log logr.Logger

	// Recorder handles logging of Kubernetes events.
	Recorder events.EventRecorder

	// DeploymentFluxGitSSHSecretReconciler has no store dependency — generates keypair locally.
	DeploymentFluxGitSSHSecretReconciler *gitFluxcd.DeploymentFluxGitSSHSecretReconciler
}

// New returns a new Mediator instance configured with the necessary dependencies.
func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, recorder events.EventRecorder) *Mediator {
	return &Mediator{
		Client:                               c,
		Scheme:                               scheme,
		Log:                                  log,
		Recorder:                             recorder,
		DeploymentFluxGitSSHSecretReconciler: gitFluxcd.NewDeploymentFluxGitSSHSecretReconciler(c, log),
	}
}

// EnsurePrerequisites provisions the prerequisites a Deployment requires
// before execution: the git SSH secret, the Flux git SSH keypair, the GitOps
// manifests repository, and the runtime infrastructure. Called from the
// domain's CmdCreate/CmdUpdate branch after resolution succeeds. Provisioning
// is fail-fast — the first failing step returns its error and the domain
// records DeploymentPrerequisitesCreateFailed.
func (m *Mediator) EnsurePrerequisites(ctx context.Context, resolved *deploymentResolution.ResolvedDeployment) error {
	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedDeployment (resolver bug)")
	}
	deploy := resolved.Deployment
	log := m.Log.WithValues("deployment", deploy.Name, "namespace", deploy.Namespace)
	log.Info("ensuring deployment prerequisites")
	// Step 0: Environment lookup
	// Environment must pre-exist — it is the root of the delivery chain and
	// the sole authority for the ClusterSecretStore binding.
	envCtx, err := query.Lookup(ctx, m.Client, deploy.Namespace, deploy.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	log.Info("environment context resolved", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	// Stage 1: Git SSH secret (store-dependent)
	//
	// ManifestsRepo is documented-optional in resolution — a Deployment with
	// no separate GitOps manifests repo (e.g. Imperative/runtime-only
	// delivery) legitimately omits it. Gated the same way as Stage 3 below;
	// without this check, the reconciler dereferences the nil
	// ManifestsRepo.CloneSecret unconditionally.
	if resolved.Spec.ManifestsRepo != nil {
		gitSSH := gitDeployment.NewDeploymentGitSSHSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
		if err := gitSSH.Reconcile(ctx, resolved); err != nil {
			return fmt.Errorf("reconcile git ssh secret: %w", err)
		}
	}
	// Stage 2: Flux SSH secret (no store — keypair generated locally)
	if err := m.DeploymentFluxGitSSHSecretReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile fluxcd git ssh secret: %w", err)
	}
	// Stage 3: GitOps manifests repo
	if resolved.Spec.ManifestsRepo != nil {
		if err := m.ensureManifestsRepo(ctx, resolved); err != nil {
			return err
		}
	}
	// Stage 4: Runtime infra
	if err := m.ensureRuntime(ctx, resolved); err != nil {
		return err
	}
	log.Info("all deployment prerequisites satisfied")
	return nil
}

// CleanupPrerequisites reverses EnsurePrerequisites — tears down the GitOps
// manifests repository (remote and local clone), the Flux git SSH keypair,
// and the git SSH secret this mediator provisioned. Called from the domain's
// CmdDelete branch, gated by the finalizer at the controller level. Teardown
// runs in reverse provisioning order. All teardown steps are attempted
// regardless of individual failures, and errors are aggregated — a stuck
// repository deletion shouldn't block cleanup of the secrets. Any returned
// error keeps the finalizer in place for retry on next reconcile.
func (m *Mediator) CleanupPrerequisites(ctx context.Context, resolved *deploymentResolution.ResolvedDeployment) error {
	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedDeployment (resolver bug)")
	}
	deploy := resolved.Deployment
	log := m.Log.WithValues("deployment", deploy.Name, "namespace", deploy.Namespace)
	log.Info("cleaning up deployment prerequisites")
	// Step 0: Environment lookup
	// Same store binding used at creation time — needed so the reconcilers
	// target the correct ClusterSecretStore-scoped resources on teardown.
	envCtx, err := query.Lookup(ctx, m.Client, deploy.Namespace, deploy.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	log.Info("environment context resolved for teardown", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	var errs []error
	// Stage 3: GitOps manifests repo
	if resolved.Spec.ManifestsRepo != nil {
		if err := m.teardownManifestsRepo(resolved); err != nil {
			errs = append(errs, fmt.Errorf("teardown manifests repo: %w", err))
		}
	}
	// Stage 2: Flux SSH secret
	if err := m.DeploymentFluxGitSSHSecretReconciler.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete fluxcd git ssh secret: %w", err))
	}
	// Stage 1: Git SSH secret — same ManifestsRepo gate as EnsurePrerequisites;
	// Delete() dereferences ManifestsRepo.CloneSecret unconditionally.
	if resolved.Spec.ManifestsRepo != nil {
		gitSSH := gitDeployment.NewDeploymentGitSSHSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
		if err := gitSSH.Delete(ctx, resolved); err != nil {
			errs = append(errs, fmt.Errorf("delete git ssh secret: %w", err))
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	log.Info("deployment prerequisites cleanup complete")
	return nil
}

// ensureRuntime verifies the runtime infrastructure a Deployment targets.
// Currently a no-op placeholder — runtime verification lands with the
// Kubernetes runtime check below.
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
