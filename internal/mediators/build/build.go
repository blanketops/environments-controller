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
Package build implements the Build prerequisite mediator.

The mediator owns the cross-cutting prerequisites a Build requires before
the application layer may act: the git SSH secret, the registry secret,
and the service account. It is invoked by the Build domain during command
handling — after resolution, before execution — and again during teardown.

Prerequisite provisioning is gated on the Environment: the Environment CR
must pre-exist as the root of the delivery chain, and it is the sole
authority for the ClusterSecretStore binding used by every store-dependent
secret this mediator reconciles.
*/
package build

import (
	"context"
	"fmt"

	"github.com/BlanketOps/environments/pkg/apis/environment/query"
	"github.com/BlanketOps/environments/pkg/secrets/git"
	"github.com/BlanketOps/environments/pkg/secrets/registry"
	serviceaccounts "github.com/BlanketOps/environments/pkg/serviceaccounts"
	buildResolution "github.com/BlanketOps/environments/resolution/build"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Mediator manages the prerequisite resources a Build depends on.
type Mediator struct {
	// Client is the Kubernetes client used for all prerequisite operations.
	Client client.Client
	// Scheme is the runtime scheme used for owner reference wiring.
	Scheme *runtime.Scheme
	// Log is the logger instance for this mediator.
	Log logr.Logger
	// Recorder handles logging of Kubernetes events.
	Recorder events.EventRecorder
	// ServiceAccountReconciler manages the build service account and its
	// secret bindings as a cross-cutting prerequisite.
	ServiceAccountReconciler *serviceaccounts.ServiceAccountReconciler
}

// New returns a new Mediator instance configured with the necessary dependencies.
func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, recorder events.EventRecorder) *Mediator {
	return &Mediator{
		Client:                   c,
		Scheme:                   scheme,
		Log:                      log,
		Recorder:                 recorder,
		ServiceAccountReconciler: serviceaccounts.NewServiceAccountReconciler(c, scheme, log),
	}
}

// EnsurePrerequisites provisions the prerequisites a Build requires before
// execution: the git SSH secret, the registry secret, and the service
// account. Called from the domain's CmdCreate/CmdUpdate branch after
// resolution succeeds. Provisioning is fail-fast — the first failing step
// returns its error and the domain records BuildPrerequisitesCreateFailed.
func (m *Mediator) EnsurePrerequisites(ctx context.Context, resolved *buildResolution.ResolvedBuild) error {
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Environment must pre-exist — it is the root of the delivery chain and
	// the sole authority for the ClusterSecretStore binding.
	// ------------------------------------------------
	envCtx, err := query.Lookup(ctx, m.Client, resolved.Build.Namespace, resolved.Build.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)

	// ------------------------------------------------------------------------------------------------------------
	// Stage 1: Git SSH secret
	// ------------------------------------------------------------------------------------------------------------
	gitSSH := git.NewBuildGitSSHSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
	if err := gitSSH.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile git ssh secret: %w", err)
	}

	// ------------------------------------------------------------------------------------------------------------
	// Stage 2: Registry secret
	// ------------------------------------------------------------------------------------------------------------
	reg := registry.NewBuildRegistryExternalSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
	if err := reg.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile registry secret: %w", err)
	}

	// ------------------------------------------------------------------------------------------------------------
	// Stage 3: Service account
	// ------------------------------------------------------------------------------------------------------------
	if err := m.ServiceAccountReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile service account: %w", err)
	}

	return nil
}

// CleanupPrerequisites reverses EnsurePrerequisites — deletes the git SSH
// secret, registry secret, and service account this mediator provisioned.
// Called from the domain's CmdDelete branch, gated by the finalizer at the
// controller level. Teardown runs in reverse provisioning order. All three
// teardown steps are attempted regardless of individual failures, and errors
// are aggregated — a stuck registry secret shouldn't block cleanup of the SA
// or git secret. Any returned error keeps the finalizer in place for retry
// on next reconcile.
func (m *Mediator) CleanupPrerequisites(ctx context.Context, resolved *buildResolution.ResolvedBuild) error {
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Same store binding used at creation time — needed so the reconcilers
	// target the correct ClusterSecretStore-scoped resources on teardown.
	// ------------------------------------------------
	envCtx, err := query.Lookup(ctx, m.Client, resolved.Build.Namespace, resolved.Build.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved for teardown", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)

	var errs []error

	// ------------------------------------------------------------------------------------------------------------
	// Stage 3: Service account
	// ------------------------------------------------------------------------------------------------------------
	if err := m.ServiceAccountReconciler.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete service account: %w", err))
	}

	// ------------------------------------------------------------------------------------------------------------
	// Stage 2: Registry secret
	// ------------------------------------------------------------------------------------------------------------
	reg := registry.NewBuildRegistryExternalSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
	if err := reg.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete registry secret: %w", err))
	}

	// ------------------------------------------------------------------------------------------------------------
	// Stage 3:  Git SSH secret
	// ------------------------------------------------------------------------------------------------------------
	gitSSH := git.NewBuildGitSSHSecretReconciler(m.Client, m.Log, envCtx.StoreName, envCtx.StoreKind)
	if err := gitSSH.Delete(ctx, resolved); err != nil {
		errs = append(errs, fmt.Errorf("delete git ssh secret: %w", err))
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return nil
}
