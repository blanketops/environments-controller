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

func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, rec record.EventRecorder,
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

// ==============================
// ENTRY POINT
// ==============================
//

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *gitrepoResolution.ResolvedGitRepository,
) error {

	repo := resolved.Repository
	log := m.Log.WithValues(
		"event", resolved.Repository.Name,
		"namespace", resolved.Repository.Namespace,
	)

	log.Info("mediator start")

	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedGitRepository (resolver bug)")
	}

	// ---------------------------------------------------------------------
	// 1. GitHub provider credentials (ExternalSecret -> Secret)
	// ---------------------------------------------------------------------
	log.Info("ensuring github provider credentials")
	if err := m.GitHubProviderSecretReconciler.Reconcile(ctx); err != nil {
		log.Error(err, "ensuring github provider credentials reconcile failed")
		if m.Recorder != nil {
			m.Recorder.Event(
				resolved.Repository,
				corev1.EventTypeWarning,
				"GitHubCrossplaneCredentialsFailed",
				err.Error(),
			)
		}

		return fmt.Errorf("github provider credentials: %w", err)
	}
	log.Info("github provider credentials ensured")

	// ---------------------------------------------------------------------
	// 2. GitHub ProviderConfig (binds provider to credentials)
	// ---------------------------------------------------------------------
	log.Info("ensuring github upjet provider")
	if err := m.GitHubProviderConfigReconciler.Reconcile(ctx); err != nil {
		log.Error(err, "ensuring github upjet provider reconcile failed")
		if m.Recorder != nil {
			m.Recorder.Event(
				resolved.Repository,
				corev1.EventTypeWarning,
				"GitHubCrossplaneProviderFailed",
				err.Error(),
			)
		}
		return fmt.Errorf("github providerconfig: %w", err)
	}

	log.Info("github upjet provider ensured")

	// ---------------------------------------------------------------------
	// 3. Webhook URL secret (per GitRepository)
	// ---------------------------------------------------------------------
	log.Info("ensuring webhook url secret")
	if err := m.HookURLSecretReconciler.Reconcile(ctx, repo); err != nil {
		log.Error(err, "ensuring webhook url reconcile failed")
		if m.Recorder != nil {
			m.Recorder.Event(
				resolved.Repository,
				corev1.EventTypeWarning,
				"WebhookHookURLSecretFailed",
				err.Error(),
			)
		}
		return fmt.Errorf("webhook url secret: %w", err)
	}

	log.Info("webhook url secret ensured")

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
