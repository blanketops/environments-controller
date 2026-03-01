package githubevents

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/github"
	githubeventResolution "github.com/ntlaletsi70/blanketops-environments/resolution/githubevent"

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

	GitHubWebhookSecretReconciler *github.GitHubWebhookSecretReconciler
	// EventSourceReconciler will come next
}

func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	rec record.EventRecorder,
) *Mediator {
	return &Mediator{
		Client:                        c,
		Scheme:                        scheme,
		Log:                           log,
		Recorder:                      rec,
		GitHubWebhookSecretReconciler: github.NewGitHubWebhookSecretReconciler(c, log),
	}
}

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *githubeventResolution.ResolvedGitHubEvent,
) error {

	if resolved == nil || resolved.Event == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedGitHubEvent provided to mediator")
	}

	// 1️⃣ GitHub webhook secret (Argo Events requirement)
	if err := m.GitHubWebhookSecretReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("github webhook secret: %w", err)
	}

	// Record Kubernetes Event against the CR (observability only)
	if m.Recorder != nil {
		m.Recorder.Event(
			resolved.Event,
			corev1.EventTypeNormal,
			"PrerequisitesReady",
			"GitHubEvent prerequisites ensured",
		)
	}

	return nil
}
