package githubevent

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

func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, rec record.EventRecorder) *Mediator {
	return &Mediator{
		Client:                        c,
		Scheme:                        scheme,
		Log:                           log,
		Recorder:                      rec,
		GitHubWebhookSecretReconciler: github.NewGitHubWebhookSecretReconciler(c, log),
	}
}

//
// ==============================
// ENTRY POINT
// ==============================
//

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *githubeventResolution.ResolvedGitHubEvent,
) error {

	log := m.Log.WithValues(
		"event", resolved.Event.Name,
		"namespace", resolved.Event.Namespace,
	)

	log.Info("mediator start")

	log.Info("ensuring github webhook secret")

	if err := m.GitHubWebhookSecretReconciler.Reconcile(ctx, resolved); err != nil {
		log.Error(err, "github webhook secret reconcile failed")

		if m.Recorder != nil {
			m.Recorder.Event(
				resolved.Event,
				corev1.EventTypeWarning,
				"WebhookSecretFailed",
				err.Error(),
			)
		}

		return fmt.Errorf("github webhook secret: %w", err)
	}

	log.Info("github webhook secret ensured")

	if m.Recorder != nil {
		m.Recorder.Event(
			resolved.Event,
			corev1.EventTypeNormal,
			"PrerequisitesReady",
			"GitHubEvent prerequisites ensured",
		)
	}

	log.Info("mediator done")

	return nil
}
