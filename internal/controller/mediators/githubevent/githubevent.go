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

package githubevents

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/github"
	githubeventResolution "github.com/ntlaletsi70/blanketops-environments/resolution/githubevent"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder events.EventRecorder

	GitHubWebhookSecretReconciler *github.GitHubWebhookSecretReconciler
	// EventSourceReconciler will come next
}

func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	rec events.EventRecorder,
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
	return nil
}
