package gitrepository

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	providerconfig "github.com/ntlaletsi70/blanketops-environments/pkg/providerconfig"
	github "github.com/ntlaletsi70/blanketops-environments/pkg/secrets/github"
	gitrepoResolution "github.com/ntlaletsi70/blanketops-environments/resolution/gitrepository"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder

	// Platform prerequisites
	GitHubProviderSecretReconciler *github.GitHubProviderSecretReconciler
	GitHubProviderConfigReconciler *providerconfig.GitHubProviderConfigReconciler
	HookURLSecretReconciler        *github.HookURLExternalSecretReconciler
}

func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	rec record.EventRecorder,
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

	// ---------------------------------------------------------------------
	// Events
	// ---------------------------------------------------------------------
	if m.Recorder != nil {
		m.Recorder.Event(
			repo,
			corev1.EventTypeNormal,
			"PrerequisitesReady",
			"GitHub provider and secrets ensured",
		)
	}

	return nil
}
