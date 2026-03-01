package packages

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/git"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/registry"
	serviceaccounts "github.com/ntlaletsi70/blanketops-environments/pkg/serviceaccounts"
	packageResolution "github.com/ntlaletsi70/blanketops-environments/resolution/packages"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder

	//PackageReconciler               *secrets.PackageReconciler
	PackageRegistrySecretReconciler        *registry.PackageRegistrySecretReconciler
	PackageStateRepositorySecretReconciler *git.PackageStateRepositorySecretReconciler
	ServiceAccountReconciler               *serviceaccounts.ServiceAccountReconciler
}

func New(
	c client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
	recorder record.EventRecorder,
) *Mediator {
	return &Mediator{
		Client:   c,
		Scheme:   scheme,
		Log:      log,
		Recorder: recorder,
		//PackageReconciler:               secrets.NewPackageReconciler(c, scheme, log),
		PackageRegistrySecretReconciler:        registry.NewPackageRegistrySecretReconciler(c, log),
		PackageStateRepositorySecretReconciler: git.NewPackageStateRepositorySecretReconciler(c, log),

		ServiceAccountReconciler: serviceaccounts.NewServiceAccountReconciler(c, scheme, log),
	}
}

// EnsurePrerequisites guarantees that all secrets and accounts required
// to execute a Package are present.
// EnsurePrerequisites guarantees that all execution prerequisites
// for a Package are materialized and usable.
func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *packageResolution.ResolvedPackage,
) error {

	// ------------------------------------------------------------
	// 0. Ensure Environment aggregate exists
	// ------------------------------------------------------------
	// if err := m.ensureAndPatchEnvironment(ctx, packages, spec); err != nil {
	// 	return fmt.Errorf("ensure environment: %w", err)
	// }

	// ------------------------------------------------------------
	// 1. ServiceAccount (execution identity)
	// ------------------------------------------------------------
	// if err := m.ServiceAccountReconciler.Reconcile(ctx, resolved); err != nil {
	// 	return fmt.Errorf("reconcile service account: %w", err)
	// }
	// ------------------------------------------------------------
	// 2. Git credentials (state repo)
	// ------------------------------------------------------------
	if secret := resolved.Spec.StateRepository.CloneSecret; secret != "" {
		if err := m.PackageStateRepositorySecretReconciler.Reconcile(ctx, resolved); err != nil {
			return fmt.Errorf("reconcile state repository credentials: %w", err)
		}
	}

	// ------------------------------------------------------------
	// 3. Registry credentials (package repository)
	// ------------------------------------------------------------
	if secret := resolved.Spec.PackageRepository.CredentialsSecret; secret != "" {
		if err := m.PackageRegistrySecretReconciler.Reconcile(ctx, resolved); err != nil {
			return fmt.Errorf("reconcile registry credentials: %w", err)
		}
	}

	return nil
}
