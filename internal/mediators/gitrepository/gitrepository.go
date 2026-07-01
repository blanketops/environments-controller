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

package gitrepository

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/environment/query"
	providerconfig "github.com/ntlaletsi70/blanketops-environments/pkg/providerconfig"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/github"
	gitrepoResolution "github.com/ntlaletsi70/blanketops-environments/resolution/gitrepository"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder events.EventRecorder
	// GitHubProviderConfigReconciler has no store dependency.
	GitHubProviderConfigReconciler *providerconfig.GitHubProviderConfigReconciler
}

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

func (m *Mediator) EnsurePrerequisites(ctx context.Context, resolved *gitrepoResolution.ResolvedGitRepository) error {
	repo := resolved.Repository

	// ── Step 0: Environment lookup ────────────────────────────────────────────
	envCtx, err := query.Lookup(ctx, m.Client, repo.Namespace, repo.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}

	m.Log.Info("environment context resolved",
		"environment", envCtx.Name,
		"type", envCtx.EnvironmentType,
		"store", envCtx.StoreName,
	)

	// ── Step 1: GitHub provider credentials (store-dependent) ─────────────────
	providerSecret := github.NewGitHubProviderSecretReconciler(m.Client, m.Log, envCtx.StoreName)
	if err := providerSecret.Reconcile(ctx); err != nil {
		return fmt.Errorf("github provider credentials: %w", err)
	}

	// ── Step 2: GitHub ProviderConfig (no store dependency) ───────────────────
	if err := m.GitHubProviderConfigReconciler.Reconcile(ctx); err != nil {
		return fmt.Errorf("github providerconfig: %w", err)
	}

	// ── Step 3: Webhook URL secret (store-dependent, per GitRepository) ───────
	hookURL := github.NewHookURLExternalSecretReconciler(m.Client, m.Log, envCtx.StoreName)
	if err := hookURL.Reconcile(ctx, repo); err != nil {
		return fmt.Errorf("hookurl secret: %w", err)
	}

	return nil
}
