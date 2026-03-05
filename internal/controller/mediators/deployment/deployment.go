package deployment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"

	env1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/pkg/secrets/git"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"
	environmentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/environment"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder record.EventRecorder

	DeploymentGitSSHSecretReconciler     *git.DeploymentGitSSHSecretReconciler
	DeploymentFluxGitSSHSecretReconciler *git.DeploymentFluxGitSSHSecretReconciler
}

func New(c client.Client, scheme *runtime.Scheme, log logr.Logger, Recorder record.EventRecorder) *Mediator {
	return &Mediator{
		Client:                               c,
		Scheme:                               scheme,
		Log:                                  log,
		Recorder:                             Recorder,
		DeploymentGitSSHSecretReconciler:     git.NewDeploymentGitSSHSecretReconciler(c, log),
		DeploymentFluxGitSSHSecretReconciler: git.NewDeploymentFluxGitSSHSecretReconciler(c, log),
	}
}

//
// ==============================
// ENTRY POINT
// ==============================
//

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) error {

	log := m.Log.WithValues(
		"deployment", resolved.Deployment.Name,
		"namespace", resolved.Deployment.Namespace,
	)

	log.Info("mediator start")

	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedDeployment (resolver bug)")
	}

	spec := resolved.Spec

	log.Info("ensuring deployment git ssh secret")
	if err := m.DeploymentGitSSHSecretReconciler.Reconcile(ctx, resolved); err != nil {
		log.Error(err, "deployment git ssh secret reconcile failed")
		if m.Recorder != nil {
			m.Recorder.Event(
				resolved.Deployment,
				corev1.EventTypeWarning,
				"DeploymentGitSSHSecretFailed",
				err.Error(),
			)
		}
		return fmt.Errorf("deployment git ssh secret: %w", err)
	}

	log.Info("deployment git ssh secret ensured")

	log.Info("ensuring deployment git fluxcd ssh secret")
	if err := m.DeploymentFluxGitSSHSecretReconciler.Reconcile(ctx, resolved); err != nil {
		log.Error(err, "deployment git fluxcd ssh secret reconcile failed")
		if m.Recorder != nil {
			m.Recorder.Event(
				resolved.Deployment,
				corev1.EventTypeWarning,
				"DeploymentFluxGitSSHSecretFailed",
				err.Error(),
			)
		}
		return fmt.Errorf("reconcile git fluxcd ssh secret: %w", err)
	}

	log.Info("deployment git fluxcd ssh secret ensured")
	// ------------------------------------------------
	// GitOps manifests repo
	// ------------------------------------------------
	log.Info("ensuring manifets repo")
	if spec.ManifestsRepo != nil {
		if err := m.ensureManifestsRepo(ctx, resolved); err != nil {
			log.Error(err, "manifests repoistory reconcile failed")

			if m.Recorder != nil {
				m.Recorder.Event(
					resolved.Deployment,
					corev1.EventTypeWarning,
					"EnsureManifestsRepoFailed",
					err.Error(),
				)
			}
			return err
		}
		log.Info("manifests repoistory ensured")
	}

	// ------------------------------------------------
	// Runtime infra
	// ------------------------------------------------
	if err := m.ensureRuntime(ctx, resolved); err != nil {
		return err
	}

	log.Info("All deployment prerequisites satisfied")
	return nil
}

//
// ==============================
// RUNTIME ENSURE (DOMAIN TYPE ONLY)
// ==============================
//

func (m *Mediator) ensureRuntime(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) error {

	return nil
}

//
// ==============================
// KUBERNETES RUNTIME CHECK
// ==============================
//

func (m *Mediator) ensureKubernetesRuntime(
	ctx context.Context,
	namespace string,
) error {

	m.Log.Info(
		"Ensuring Kubernetes runtime",
		"namespace", namespace,
	)

	ns := &corev1.Namespace{}
	err := m.Client.Get(
		ctx,
		client.ObjectKey{Name: namespace},
		ns,
	)

	if err != nil {
		return fmt.Errorf(
			"kubernetes namespace %q not available: %w",
			namespace,
			err,
		)
	}

	return nil
}

//
// ==============================
// ENVIRONMENT AGGREGATE
// ==============================
//

func toRawContract(
	spec *environmentResolution.ResolvedEnvironmentSpec,
) (runtime.RawExtension, error) {

	contract := spec.ToEnvironmentContract()

	raw, err := json.Marshal(contract)
	if err != nil {
		return runtime.RawExtension{}, err
	}

	return runtime.RawExtension{Raw: raw}, nil
}

func (m *Mediator) ensureAndPatchEnvironment(
	ctx context.Context,
	resolved *deploymentResolution.ResolvedDeployment,
) error {

	deploy := resolved.Deployment
	labels := deploy.GetLabels()

	envName := labels["environments.blanketops.dev/name"]
	envType := labels["environments.blanketops.dev/type"]

	if envName == "" || envType == "" {
		return nil
	}

	key := client.ObjectKey{
		Name:      envName,
		Namespace: deploy.Namespace,
	}

	var env env1alpha1.Environment
	err := m.Client.Get(ctx, key, &env)

	if apierrors.IsNotFound(err) {

		spec := &environmentResolution.ResolvedEnvironmentSpec{
			ApplicationName: envName,
			EnvironmentType: envType,
			Deployment:      deploy.Name,
		}

		raw, err := toRawContract(spec)
		if err != nil {
			return err
		}

		env = env1alpha1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      envName,
				Namespace: deploy.Namespace,
				Labels: map[string]string{
					"environments.blanketops.dev/name": envName,
					"environments.blanketops.dev/type": envType,
				},
			},
			Spec: env1alpha1.EnvironmentSpec{
				Contract: raw,
			},
		}

		return m.Client.Create(ctx, &env)
	}

	if err != nil {
		return err
	}

	resolvedEnv, err := environmentResolution.ResolveEnvironment(&env)
	if err != nil {
		return err
	}

	if resolvedEnv.Spec.Deployment == deploy.Name {
		return nil
	}

	resolvedEnv.Spec.Deployment = deploy.Name

	raw, err := toRawContract(resolvedEnv.Spec)
	if err != nil {
		return err
	}

	env.Spec.Contract = raw
	return m.Client.Update(ctx, &env)
}
