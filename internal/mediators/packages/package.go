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

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *packageResolution.ResolvedPackage,
) error {
	if resolved == nil || resolved.Package == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedPackage (resolver bug)")
	}

	pkg := resolved.Package

	// ── Step 0: Environment lookup ────────────────────────────────────────────
	envCtx, err := query.Lookup(ctx, m.Client, pkg.Namespace, pkg.Labels)
	if err != nil {
		return fmt.Errorf("environment lookup: %w", err)
	}

	m.Log.Info("environment context resolved",
		"environment", envCtx.Name,
		"type", envCtx.EnvironmentType,
		"store", envCtx.StoreName,
	)

	// ── Step 1: Git credentials (state repo, store-dependent) ─────────────────
	if resolved.Spec.StateRepository.CloneSecret != "" {
		stateRepo := git.NewPackageStateRepositorySecretReconciler(m.Client, m.Log, envCtx.StoreName)
		if err := stateRepo.Reconcile(ctx, resolved); err != nil {
			return fmt.Errorf("reconcile state repository credentials: %w", err)
		}
	}

	// ── Step 2: Registry credentials (store-dependent) ────────────────────────
	if resolved.Spec.PackageRepository.CredentialsSecret != "" {
		reg := registry.NewPackageRegistrySecretReconciler(m.Client, m.Log, envCtx.StoreName)
		if err := reg.Reconcile(ctx, resolved); err != nil {
			return fmt.Errorf("reconcile registry credentials: %w", err)
		}
	}

	return nil
}
