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
Prerequisite provisioning is gated on the Environment: the Environment CR
must pre-exist as the root of the delivery chain, and it is the sole
authority for the ClusterSecretStore binding used by every store-dependent
secret this mediator reconciles.
*/
package githubevents

import (
	"context"
	"fmt"

	"github.com/blanketops/environments/pkg/apis/environment/query"
	"github.com/blanketops/environments/pkg/secrets/github"
	githubeventResolution "github.com/blanketops/environments/resolution/githubevent"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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
// CmdDelete branch, gated by the finalizer at the controller level. Teardown
// runs in reverse provisioning order. All teardown steps are attempted
// regardless of individual failures, and errors are aggregated. Any returned
// error keeps the finalizer in place for retry on next reconcile.
func (m *Mediator) CleanupPrerequisites(ctx context.Context, resolved *githubeventResolution.ResolvedGitHubEvent) error {
	if resolved == nil || resolved.Event == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedGitHubEvent provided to mediator")
	}
	event := resolved.Event
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Same store binding used at creation time — needed so the reconcilers
	// target the correct ClusterSecretStore-scoped resources on teardown.
	// ------------------------------------------------
	envCtx, err := query.Lookup(ctx, m.Client, event.Namespace, event.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved for teardown", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	var errs []error
	// ------------------------------------------------------------------------------------------------------------
	// Stage 1: GitHub webhook secret
	// ------------------------------------------------------------------------------------------------------------
	webhookSecret := github.NewGitHubWebhookSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
	if err := webhookSecret.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete github webhook secret: %w", err))
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return nil
}
