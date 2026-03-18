package build

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-logr/logr"

	env1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/git"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/registry"
	buildResolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	environmentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/environment"

	serviceaccounts "github.com/ntlaletsi70/blanketops-environments/pkg/serviceaccounts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder events.EventRecorder

	BuildGitSSHSecretReconciler      *git.BuildGitSSHSecretReconciler
	RegistryExternalSecretReconciler *registry.BuildRegistryExternalSecretReconciler
	ServiceAccountReconciler         *serviceaccounts.ServiceAccountReconciler
}

func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, Recorder events.EventRecorder) *Mediator {
	return &Mediator{
		Client:                           c,
		Scheme:                           scheme,
		Log:                              log,
		Recorder:                         Recorder,
		BuildGitSSHSecretReconciler:      git.NewBuildGitSSHSecretReconciler(c, log),
		RegistryExternalSecretReconciler: registry.NewBuildRegistryExternalSecretReconciler(c, log),
		ServiceAccountReconciler:         serviceaccounts.NewServiceAccountReconciler(c, scheme, log),
	}
}

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *buildResolution.ResolvedBuild,
) error {
	// build := resolved.Build
	// spec := resolved.Spec

	// -------------------------------------------------
	// Ensure + Patch Environment aggregate
	// -------------------------------------------------
	// if err := m.ensureAndPatchEnvironment(ctx, build, spec); err != nil {
	// 	return fmt.Errorf("ensure environment: %w", err)
	// }

	// -------------------------------------------------
	// Prerequisites
	// -------------------------------------------------

	if err := m.BuildGitSSHSecretReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile git ssh secret: %w", err)
	}

	if err := m.RegistryExternalSecretReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile registry secret: %w", err)
	}

	if err := m.ServiceAccountReconciler.Reconcile(ctx, resolved); err != nil {
		return fmt.Errorf("reconcile service account: %w", err)
	}

	return nil
}

func ToRawContract(spec *environmentResolution.ResolvedEnvironmentSpec) (runtime.RawExtension, error) {
	contract := spec.ToEnvironmentContract()

	raw, err := json.Marshal(contract)
	if err != nil {
		return runtime.RawExtension{}, err
	}

	return runtime.RawExtension{Raw: raw}, nil
}
func EnvironmentSpecFromBuild(
	build *env1alpha1.Build,
	rb *buildResolution.ResolvedBuildSpec,
) *environmentResolution.ResolvedEnvironmentSpec {

	labels := build.GetLabels()

	return &environmentResolution.ResolvedEnvironmentSpec{
		ApplicationName: labels["environments.blanketops.dev/name"],
		EnvironmentType: labels["environments.blanketops.dev/type"],
		GitOwner:        deriveGitOwner(rb.Source.URL),
		Branch:          rb.Source.Revision,
		Build:           build.Name,
	}
}

func deriveGitOwner(repoURL string) string {
	if repoURL == "" {
		return ""
	}

	// --- SSH form: git@github.com:owner/repo.git
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.Split(repoURL, ":")
		if len(parts) != 2 {
			return ""
		}

		path := strings.TrimSuffix(parts[1], ".git")
		segs := strings.Split(path, "/")
		if len(segs) >= 1 {
			return segs[0]
		}
	}

	// --- HTTPS form: https://github.com/owner/repo(.git)
	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		u, err := url.Parse(repoURL)
		if err != nil {
			return ""
		}

		path := strings.TrimSuffix(u.Path, ".git")
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(segs) >= 1 {
			return segs[0]
		}
	}

	return ""
}

func (m *Mediator) ensureAndPatchEnvironment(
	ctx context.Context,
	build *env1alpha1.Build,
	rb *buildResolution.ResolvedBuildSpec,
) error {

	labels := build.GetLabels()
	envName := labels["environments.blanketops.dev/name"]
	envType := labels["environments.blanketops.dev/type"]

	if envName == "" || envType == "" {
		return nil
	}

	key := client.ObjectKey{
		Name:      envName,
		Namespace: build.Namespace,
	}

	var env env1alpha1.Environment
	err := m.Client.Get(ctx, key, &env)

	// -------------------------------------------------
	// Build contribution
	// -------------------------------------------------
	contribution := EnvironmentSpecFromBuild(build, rb)

	// -------------------------------------------------
	// CREATE
	// -------------------------------------------------
	if apierrors.IsNotFound(err) {

		raw, err := ToRawContract(contribution)
		if err != nil {
			return err
		}

		env = env1alpha1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      envName,
				Namespace: build.Namespace,
				Labels: map[string]string{
					"environments.blanketops.dev/name": envName,
					"environments.blanketops.dev/type": envType,
				},
			},
			Spec: env1alpha1.EnvironmentSpec{
				Contract: raw,
			},
		}

		m.Log.Info("creating environment shell", "environment", envName)
		return m.Client.Create(ctx, &env)
	}

	if err != nil {
		return err
	}

	// -------------------------------------------------
	// PATCH (aggregate merge)
	// -------------------------------------------------
	resolvedEnv, err := environmentResolution.ResolveEnvironment(&env)
	if err != nil {
		return err
	}

	// 🔥 Aggregate mutation (build contribution)
	resolvedEnv.Spec.Build = build.Name
	resolvedEnv.Spec.GitOwner = contribution.GitOwner
	resolvedEnv.Spec.Branch = contribution.Branch
	resolvedEnv.Spec.EnvironmentType = contribution.EnvironmentType
	resolvedEnv.Spec.ApplicationName = contribution.ApplicationName

	raw, err := ToRawContract(resolvedEnv.Spec)
	if err != nil {
		return err
	}

	env.Spec.Contract = raw

	m.Log.Info("patching environment aggregate", "environment", envName)
	return m.Client.Update(ctx, &env)
}
