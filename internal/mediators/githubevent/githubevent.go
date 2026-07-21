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
Package githubevents implements the GitHubEvent prerequisite mediator.
The mediator owns the cross-cutting prerequisite a GitHubEvent requires
before the application layer may act: the GitHub webhook HMAC secret used
by the Argo Events sensor to verify payload signatures. It is invoked by
the GitHubEvent domain during command handling — after resolution, before
execution — and again during teardown.

Provisioning (EnsurePrerequisites) is gated on the Environment: the
Environment CR must pre-exist, and it is the sole authority for the
ClusterSecretStore binding the webhook secret is written through.

Teardown (CleanupPrerequisites) is deliberately NOT gated on the
Environment. GitHubEvent CRs are written by the Argo Events Sensor into the
fixed argo-events namespace, not the Environment's own namespace —
Environment is namespace-scoped and dynamic, and has no authority over
argo-events. Deleting the webhook secret only needs its name and namespace,
not a store binding, so teardown skips the lookup entirely rather than
depending on a relationship that doesn't hold for this CR.
*/
package githubevents

import (
	"context"
	"fmt"

	"github.com/blanketops/environments/pkg/apis/environment/query"
	github "github.com/blanketops/environments/pkg/secrets/github/webhook"
	githubeventResolution "github.com/blanketops/environments/resolution/githubevent/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Mediator manages the prerequisite resources a GitHubEvent depends on.
type Mediator struct {
	// Client is the Kubernetes client used for all prerequisite operations.
	Client client.Client
	// Scheme is the runtime scheme used for owner reference wiring.
	Scheme *runtime.Scheme
	// Log is the logger instance for this mediator.
	Log logr.Logger
	// Recorder handles logging of Kubernetes events.
	Recorder events.EventRecorder
}

// New returns a new Mediator instance configured with the necessary dependencies.
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
	}
}

// EnsurePrerequisites provisions the prerequisite a GitHubEvent requires
// before execution: the GitHub webhook HMAC secret. Called from the domain's
// CmdCreate/CmdUpdate branch after resolution succeeds. Provisioning is
// fail-fast — the first failing step returns its error and the domain
// records GitHubEventPrerequisitesCreateFailed.
func (m *Mediator) EnsurePrerequisites(ctx context.Context, resolved *githubeventResolution.ResolvedGitHubEvent) error {
	if resolved == nil || resolved.Event == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedGitHubEvent provided to mediator")
	}
	event := resolved.Event
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Environment must pre-exist — it is the root of the delivery chain and
	// the sole authority for the ClusterSecretStore binding.
	// ------------------------------------------------
	envCtx, err := query.Lookup(ctx, m.Client, event.Namespace, event.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	// ------------------------------------------------------------------------------------------------------------
	// Stage 1: GitHub webhook secret
	// ------------------------------------------------------------------------------------------------------------
	webhookSecret := github.NewGitHubWebhookSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
	if err := webhookSecret.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("github webhook secret: %w", err)
	}
	return nil
}

// CleanupPrerequisites reverses EnsurePrerequisites — deletes the GitHub
// webhook HMAC secret this mediator provisioned. Called from the domain's
// CmdDelete branch, gated by the finalizer at the controller level. Any
// returned error keeps the finalizer in place for retry on next reconcile.
//
// Deliberately does NOT look up the owning Environment. GitHubEvent CRs are
// written by the Argo Events Sensor into the fixed argo-events namespace,
// not the Environment's own namespace — Environment is namespace-scoped and
// dynamic, and cannot own resources living in argo-events. Requiring the
// lookup here would make it permanently unsatisfiable (or, if the labels
// happen to line up, would tie teardown to Environment lifecycle it has no
// authority over). GitHubWebhookSecretReconciler.Delete only needs the
// secret's name and namespace to remove it — the store binding was only
// ever needed to create it, not to delete it — so no store context is
// needed here either.
func (m *Mediator) CleanupPrerequisites(ctx context.Context, resolved *githubeventResolution.ResolvedGitHubEvent) error {
	if resolved == nil || resolved.Event == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedGitHubEvent provided to mediator")
	}
	// ------------------------------------------------------------------------------------------------------------
	// Stage 1: GitHub webhook secret
	// ------------------------------------------------------------------------------------------------------------
	webhookSecret := github.NewGitHubWebhookSecretReconciler(m.Client, m.Log, "", "")
	if err := webhookSecret.Delete(ctx, resolved); err != nil {
		return fmt.Errorf("delete github webhook secret: %w", err)
	}
	return nil
}
