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
	providerconfig "github.com/ntlaletsi70/blanketops-environments/pkg/providerconfig"
	github "github.com/ntlaletsi70/blanketops-environments/pkg/secrets/github"
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

	// Platform prerequisites
	GitHubProviderSecretReconciler *github.GitHubProviderSecretReconciler
	GitHubProviderConfigReconciler *providerconfig.GitHubProviderConfigReconciler
	HookURLSecretReconciler        *github.HookURLExternalSecretReconciler
}

func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	rec events.EventRecorder,
) *Mediator {
	return &Mediator{
		Client:   c,
		Scheme:   scheme,
		Log:      log,
		Recorder: rec,

		GitHubProviderSecretReconciler: github.NewGitHubProviderSecretReconciler(c, log),
		GitHubProviderConfigReconciler: providerconfig.NewGitHubProviderConfigReconciler(c, log),
		HookURLSecretReconciler:        github.NewHookURLExternalSecretReconciler(c, log),
	}
}

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *gitrepoResolution.ResolvedGitRepository,
) error {

	repo := resolved.Repository

	// ---------------------------------------------------------------------
	// 1. GitHub provider credentials (ExternalSecret -> Secret)
	// ---------------------------------------------------------------------
	if err := m.GitHubProviderSecretReconciler.Reconcile(ctx); err != nil {
		return fmt.Errorf("github provider credentials: %w", err)
	}

	// ---------------------------------------------------------------------
	// 2. GitHub ProviderConfig (binds provider to credentials)
	// ---------------------------------------------------------------------
	if err := m.GitHubProviderConfigReconciler.Reconcile(ctx); err != nil {
		return fmt.Errorf("github providerconfig: %w", err)
	}

	// ---------------------------------------------------------------------
	// 3. Webhook URL secret (per GitRepository)
	// ---------------------------------------------------------------------
	if err := m.HookURLSecretReconciler.Reconcile(ctx, repo); err != nil {
		return fmt.Errorf("hookurl secret: %w", err)
	}

	return nil
}
