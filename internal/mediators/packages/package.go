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
Package packages implements the Package prerequisite mediator.
The mediator owns the cross-cutting prerequisites a Package requires before
the application layer may act: the state repository git credentials and the
package registry credentials. Both are declared optionally on the Package
contract — a stage is skipped when its secret reference is absent. It is
invoked by the Package domain during command handling — after resolution,
before execution — and again during teardown.
Prerequisite provisioning is gated on the Environment: the Environment CR
must pre-exist as the root of the delivery chain, and it is the sole
authority for the ClusterSecretStore binding used by every store-dependent
secret this mediator reconciles.
*/
package packages

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/pkg/apis/environment/query"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/git"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/registry"
	serviceaccounts "github.com/ntlaletsi70/blanketops-environments/pkg/serviceaccounts"
	packageResolution "github.com/ntlaletsi70/blanketops-environments/resolution/packages"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Mediator manages the prerequisite resources a Package depends on.
type Mediator struct {
	// Client is the Kubernetes client used for all prerequisite operations.
	Client client.Client
	// Scheme is the runtime scheme used for owner reference wiring.
	Scheme *runtime.Scheme
	// Log is the logger instance for this mediator.
	Log logr.Logger
	// Recorder handles logging of Kubernetes events.
	Recorder events.EventRecorder
	// ServiceAccountReconciler manages the package service account and its
	// secret bindings as a cross-cutting prerequisite.
	ServiceAccountReconciler *serviceaccounts.ServiceAccountReconciler
}

// New returns a new Mediator instance configured with the necessary dependencies.
func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	recorder events.EventRecorder,
) *Mediator {
	return &Mediator{
		Client:                   c,
		Scheme:                   scheme,
		Log:                      log,
		Recorder:                 recorder,
		ServiceAccountReconciler: serviceaccounts.NewServiceAccountReconciler(c, scheme, log),
	}
}

// EnsurePrerequisites provisions the prerequisites a Package requires before
// execution: the state repository git credentials and the package registry
// credentials, each skipped when its secret reference is absent from the
// contract. Called from the domain's CmdCreate/CmdUpdate branch after
// resolution succeeds. Provisioning is fail-fast — the first failing step
// returns its error and the domain records PackagePrerequisitesCreateFailed.
func (m *Mediator) EnsurePrerequisites(ctx context.Context, resolved *packageResolution.ResolvedPackage) error {
	if resolved == nil || resolved.Package == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedPackage (resolver bug)")
	}
	pkg := resolved.Package
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Environment must pre-exist — it is the root of the delivery chain and
	// the sole authority for the ClusterSecretStore binding.
	// ------------------------------------------------
	envCtx, err := query.Lookup(ctx, m.Client, pkg.Namespace, pkg.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	// ------------------------------------------------------------------------------------------------------------
	// Stage 1: Git credentials (state repo, store-dependent)
	// ------------------------------------------------------------------------------------------------------------
	if resolved.Spec.StateRepository.CloneSecret != "" {
		stateRepo := git.NewPackageStateRepositorySecretReconciler(m.Client, m.Log, envCtx.StoreName)
		if err := stateRepo.Reconcile(ctx, resolved); err != nil {
			return fmt.Errorf("reconcile state repository credentials: %w", err)
		}
	}
	// ------------------------------------------------------------------------------------------------------------
	// Stage 2: Registry credentials (store-dependent)
	// ------------------------------------------------------------------------------------------------------------
	if resolved.Spec.PackageRepository.CredentialsSecret != "" {
		reg := registry.NewPackageRegistrySecretReconciler(m.Client, m.Log, envCtx.StoreName)
		if err := reg.Reconcile(ctx, resolved); err != nil {
			return fmt.Errorf("reconcile registry credentials: %w", err)
		}
	}
	return nil
}

// CleanupPrerequisites reverses EnsurePrerequisites — deletes the registry
// credentials and state repository git credentials this mediator
// provisioned, each skipped when its secret reference is absent from the
// contract. Called from the domain's CmdDelete branch, gated by the
// finalizer at the controller level. Teardown runs in reverse provisioning
// order. All teardown steps are attempted regardless of individual failures,
// and errors are aggregated — a stuck registry secret shouldn't block
// cleanup of the state repository credentials. Any returned error keeps the
// finalizer in place for retry on next reconcile.
func (m *Mediator) CleanupPrerequisites(ctx context.Context, resolved *packageResolution.ResolvedPackage) error {
	if resolved == nil || resolved.Package == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedPackage (resolver bug)")
	}
	pkg := resolved.Package
	// ------------------------------------------------
	// Step 0: Environment lookup
	// Same store binding used at creation time — needed so the reconcilers
	// target the correct ClusterSecretStore-scoped resources on teardown.
	// ------------------------------------------------
	envCtx, err := query.Lookup(ctx, m.Client, pkg.Namespace, pkg.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}
	m.Log.Info("environment context resolved for teardown", "environment", envCtx.Name, "type", envCtx.EnvironmentType, "store", envCtx.StoreName)
	var errs []error
	// ------------------------------------------------------------------------------------------------------------
	// Stage 2: Registry credentials
	// ------------------------------------------------------------------------------------------------------------
	if resolved.Spec.PackageRepository.CredentialsSecret != "" {
		reg := registry.NewPackageRegistrySecretReconciler(m.Client, m.Log, envCtx.StoreName)
		if err := reg.Delete(ctx, resolved); err != nil {
			errs = append(errs, fmt.Errorf("delete registry credentials: %w", err))
		}
	}
	// ------------------------------------------------------------------------------------------------------------
	// Stage 1: Git credentials (state repo)
	// ------------------------------------------------------------------------------------------------------------
	if resolved.Spec.StateRepository.CloneSecret != "" {
		stateRepo := git.NewPackageStateRepositorySecretReconciler(m.Client, m.Log, envCtx.StoreName)
		if err := stateRepo.Delete(ctx, resolved); err != nil {
			errs = append(errs, fmt.Errorf("delete state repository credentials: %w", err))
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return nil
}
