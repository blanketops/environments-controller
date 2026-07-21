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
Package gitrepository implements the GitRepository prerequisite mediator.
The mediator owns the cross-cutting prerequisites a GitRepository requires
before the application layer may act: the GitHub provider credentials, the
GitHub ProviderConfig, and the per-repository webhook URL secret. It is
invoked by the GitRepository domain during command handling — after
resolution, before execution — and again during teardown.
Prerequisite provisioning is gated on the Environment: the Environment CR
must pre-exist as the root of the delivery chain, and it is the sole
authority for the ClusterSecretStore binding used by every store-dependent
secret this mediator reconciles.
The provider credentials and ProviderConfig are shared, cluster-level
prerequisites serving every GitRepository — they are ensured per-CR but
never torn down per-CR. Only the webhook URL secret is scoped to a single
GitRepository and follows its lifecycle.
*/
package gitrepository

import (
	"context"
	"fmt"

	"github.com/blanketops/environments/pkg/apis/environment/query"
	providerconfig "github.com/blanketops/environments/pkg/providerconfig"
	githubCrossplane "github.com/blanketops/environments/pkg/secrets/github/crossplane"
	githubHookurl "github.com/blanketops/environments/pkg/secrets/github/hookurl"
	gitrepoResolution "github.com/blanketops/environments/resolution/gitrepository/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mediatorenv "github.com/blanketops/environments-controller/internal/mediators"
)

// Mediator manages the prerequisite resources a GitRepository depends on.
type Mediator struct {
	// Client is the Kubernetes client used for all prerequisite operations.
	Client client.Client
	// Scheme is the runtime scheme used for owner reference wiring.
	Scheme *runtime.Scheme
	// Log is the logger instance for this mediator.
	Log logr.Logger
	// Recorder handles logging of Kubernetes events.
	Recorder events.EventRecorder
	// GitHubProviderConfigReconciler has no store dependency.
	GitHubProviderConfigReconciler *providerconfig.GitHubProviderConfigReconciler
}

// New returns a new Mediator instance configured with the necessary dependencies.
func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	rec events.EventRecorder,
) *Mediator {
	return &Mediator{
		Client:                         c,
		Scheme:                         scheme,
		Log:                            log,
		Recorder:                       rec,
		GitHubProviderConfigReconciler: providerconfig.NewGitHubProviderConfigReconciler(c, log),
	}
}

// EnsurePrerequisites provisions the prerequisites a GitRepository requires
// before execution: the GitHub provider credentials, the GitHub
// ProviderConfig, and the per-repository webhook URL secret. Called from the
// domain's CmdCreate/CmdUpdate branch after resolution succeeds. Provisioning
// is fail-fast — the first failing step returns its error and the domain
// records GitRepositoryPrerequisitesCreateFailed.
func (m *Mediator) EnsurePrerequisites(ctx context.Context, resolved *gitrepoResolution.ResolvedGitRepository) error {
	if resolved == nil || resolved.Repository == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedGitRepository provided to mediator")
	}
	repo := resolved.Repository
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Environment must pre-exist — it is the root of the delivery chain and
	// the sole authority for the ClusterSecretStore binding.
	// ------------------------------------------------
	envCtx, err := query.Lookup(ctx, m.Client, repo.Namespace, repo.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	// ------------------------------------------------------------------------------------------------------------
	// Stage 1: GitHub provider credentials (store-dependent, shared)
	// ------------------------------------------------------------------------------------------------------------
	providerSecret := githubCrossplane.NewGitHubProviderSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
	if err := providerSecret.Reconcile(ctx); err != nil {
		return fmt.Errorf("github provider credentials: %w", err)
	}
	// ------------------------------------------------------------------------------------------------------------
	// Stage 2: GitHub ProviderConfig (no store dependency, shared)
	// ------------------------------------------------------------------------------------------------------------
	if err := m.GitHubProviderConfigReconciler.Reconcile(ctx); err != nil {
		return fmt.Errorf("github providerconfig: %w", err)
	}
	// ------------------------------------------------------------------------------------------------------------
	// Stage 3: Webhook URL secret (CR-sourced, per GitRepository)
	// ------------------------------------------------------------------------------------------------------------
	hookURL := githubHookurl.NewHookURLSecretReconciler(m.Client, m.Scheme, m.Log)
	if err := hookURL.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("hookurl secret: %w", err)
	}
	return nil
}

// CleanupPrerequisites reverses the per-CR portion of EnsurePrerequisites —
// deletes the webhook URL secret this mediator provisioned for the
// GitRepository. Called from the domain's CmdDelete branch, gated by the
// finalizer at the controller level. Teardown runs in reverse provisioning
// order. All teardown steps are attempted regardless of individual failures,
// and errors are aggregated. Any returned error keeps the finalizer in place
// for retry on next reconcile.
//
// The GitHub provider credentials (Stage 1) and ProviderConfig (Stage 2) are
// deliberately NOT torn down here: they are shared, cluster-level
// prerequisites serving every GitRepository. Deleting them when one CR is
// torn down would sever provider access for all others. Their removal
// belongs to platform-level uninstall paths only.
func (m *Mediator) CleanupPrerequisites(ctx context.Context, resolved *gitrepoResolution.ResolvedGitRepository) error {
	if resolved == nil || resolved.Repository == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedGitRepository provided to mediator")
	}
	repo := resolved.Repository
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Same store binding used at creation time — needed so the reconcilers
	// target the correct ClusterSecretStore-scoped resources on teardown.
	//
	// If the Environment was already deleted (e.g. out-of-order deletion
	// alongside its children), query.Lookup below fails permanently and
	// would otherwise leave this GitRepository's finalizer stuck forever.
	// Skip store-bound cleanup in that case and let the finalizer proceed.
	// ------------------------------------------------
	gone, err := mediatorenv.EnvironmentGone(ctx, m.Client, repo.Namespace, repo.Labels)
	if err != nil {
		return fmt.Errorf("environment existence check: %w", err)
	}
	if gone {
		m.Log.Info("environment already deleted, skipping store-bound cleanup", "namespace", repo.Namespace)
		return nil
	}
	envCtx, err := query.Lookup(ctx, m.Client, repo.Namespace, repo.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved for teardown", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	var errs []error
	// ------------------------------------------------------------------------------------------------------------
	// Stage 3: Webhook URL secret
	// ------------------------------------------------------------------------------------------------------------
	hookURL := githubHookurl.NewHookURLSecretReconciler(m.Client, m.Scheme, m.Log)
	if err := hookURL.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete hookurl secret: %w", err))
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return nil
}
