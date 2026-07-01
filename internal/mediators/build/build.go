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
package build

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/environment/query"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/git"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/registry"
	serviceaccounts "github.com/ntlaletsi70/blanketops-environments/pkg/serviceaccounts"
	buildResolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client                   client.Client
	Scheme                   *runtime.Scheme
	Log                      logr.Logger
	Recorder                 events.EventRecorder
	ServiceAccountReconciler *serviceaccounts.ServiceAccountReconciler
}

func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, recorder events.EventRecorder) *Mediator {
	return &Mediator{
		Client:                   c,
		Scheme:                   scheme,
		Log:                      log,
		Recorder:                 recorder,
		ServiceAccountReconciler: serviceaccounts.NewServiceAccountReconciler(c, scheme, log),
	}
}

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *buildResolution.ResolvedBuild,
) error {
	// ── Step 0: Environment lookup ────────────────────────────────────────────
	// Environment must pre-exist — it is the root of the delivery chain and
	// the sole authority for the ClusterSecretStore binding.
	envCtx, err := query.Lookup(ctx, m.Client, resolved.Build.Namespace, resolved.Build.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}

	m.Log.Info("environment context resolved",
		"environment", envCtx.Name,
		"type", envCtx.EnvironmentType,
		"store", envCtx.StoreName,
	)

	// ── Step 1: Git SSH secret ────────────────────────────────────────────────
	gitSSH := git.NewBuildGitSSHSecretReconciler(m.Client, m.Log, envCtx.StoreName)
	if err := gitSSH.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile git ssh secret: %w", err)
	}

	// ── Step 2: Registry secret ───────────────────────────────────────────────
	reg := registry.NewBuildRegistryExternalSecretReconciler(m.Client, m.Log, envCtx.StoreName)
	if err := reg.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile registry secret: %w", err)
	}

	// ── Step 3: Service account ───────────────────────────────────────────────
	if err := m.ServiceAccountReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile service account: %w", err)
	}

	return nil
}

// CleanupPrerequisites reverses EnsurePrerequisites — deletes the git SSH
// secret, registry secret, and service account this mediator provisioned.
// Called from the domain's CmdDelete branch, gated by the finalizer at the
// controller level. All three teardown steps are attempted regardless of
// individual failures, and errors are aggregated — a stuck registry secret
// shouldn't block cleanup of the SA or git secret. Any returned error keeps
// the finalizer in place for retry on next reconcile.
func (m *Mediator) CleanupPrerequisites(
	ctx context.Context,
	resolved *buildResolution.ResolvedBuild,
) error {
	// ── Step 0: Environment lookup ────────────────────────────────────────────
	// Same store binding used at creation time — needed so the reconcilers
	// target the correct ClusterSecretStore-scoped resources on teardown.
	envCtx, err := query.Lookup(ctx, m.Client, resolved.Build.Namespace, resolved.Build.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved for teardown",
		"environment", envCtx.Name,
		"type", envCtx.EnvironmentType,
		"store", envCtx.StoreName,
	)

	var errs []error

	// ── Step 1: Service account ───────────────────────────────────────────────
	if err := m.ServiceAccountReconciler.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete service account: %w", err))
	}

	// ── Step 2: Registry secret ───────────────────────────────────────────────
	reg := registry.NewBuildRegistryExternalSecretReconciler(m.Client, m.Log, envCtx.StoreName)
	if err := reg.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete registry secret: %w", err))
	}

	// ── Step 3: Git SSH secret ────────────────────────────────────────────────
	gitSSH := git.NewBuildGitSSHSecretReconciler(m.Client, m.Log, envCtx.StoreName)
	if err := gitSSH.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete git ssh secret: %w", err))
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return nil
}
