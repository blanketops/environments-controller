/*
Copyright 2026.

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

package environments

import (
	"context"
	"reflect"

	"github.com/go-logr/logr"
	deploymentv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	libdeployment "github.com/ntlaletsi70/blanketops-environments/cache/deployment"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"github.com/ntlaletsi70/blanketops-environments/pkg/deployment/api"
	"github.com/ntlaletsi70/blanketops-environments/pkg/deployment/application"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	deploydomain "github.com/ntlaletsi70/blanketops-environments-controller/internal/domains/deployment"
	deployment "github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/deployment"
	runtimeinfra "github.com/ntlaletsi70/blanketops-environments-controller/internal/runtime"
)

// DeploymentReconciler reconciles a Deployment object
type DeploymentReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Log                logr.Logger
	deploymentCache    *libdeployment.DeploymentCache
	reader             client.Reader
	Recorder           events.EventRecorder
	Runtime            *runtimeinfra.Runtime
	DeploymentMediator *deployment.Mediator
	DeploymentService  *application.DeploymentService
}

// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments/finalizers,verbs=update

// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories;helmrepositories;ocirepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kappctrl.k14s.io,resources=apps,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories/status,verbs=get;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Deployment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *DeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "deployment", "namespace", req.Namespace, "name", req.Name)
	ctx = logr.NewContext(ctx, log)
	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch Deployment
	// ------------------------------------------------
	var deployment deploymentv1alpha1.Deployment
	if err := r.Get(ctx, req.NamespacedName, &deployment); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: deployment not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch deployment")
		return ctrl.Result{}, err
	}

	log.Info("deployment fetched", "generation", deployment.Generation, "resourceVersion", deployment.ResourceVersion)

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------

	cmd := core.Command{
		GVK:  deploymentv1alpha1.GroupVersion.WithKind("Deployment"),
		Type: core.CmdUpdate,
		Obj:  &deployment,
	}

	log.Info("routing deployment to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {

		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&deployment, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		log.Info("reconcile exit: engine error")

		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Persist status (retry-on-conflict)
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest deploymentv1alpha1.Deployment
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		if reflect.DeepEqual(latest.Status, deployment.Status) {
			return nil
		}

		latest.Status = deployment.Status
		return r.Status().Update(ctx, &latest)

	}); err != nil {
		log.Error(err, "failed to update deployement status")
		return ctrl.Result{}, err
	}

	log.Info("deployement status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	//---------------------------------------------------------------------
	// Logging & events
	//---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("Deployment")
	r.Recorder = mgr.GetEventRecorder("deployment-controller")

	//---------------------------------------------------------------------
	// Runtime Infrastructure
	//---------------------------------------------------------------------
	cache := r.Runtime.Cache
	events := r.Runtime.Events
	registry := r.Runtime.Registry

	// ---------------------------------------------------------------------
	// Mediator (infra / prerequisites only)
	// ---------------------------------------------------------------------
	r.DeploymentMediator = deployment.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.deployment"), r.Recorder)

	// ---------------------------------------------------------------------
	// Providers (runtime backends)
	// ---------------------------------------------------------------------
	// kubernetesBackend := api.NewK8SProvider(
	// 	mgr.GetClient(),
	// 	mgr.GetScheme(),
	// 	r.Log.WithName("backend.kubernetes"),
	// 	r.Recorder,
	// )

	// Future-safe placeholders
	// knativeBackend := application.NewKnativeBackend(...)
	// ecsBackend := application.NewECSBackend(...)
	// fluxBackend := application.NewFluxBackend(...)

	// ---------------------------------------------------------------------
	// Runtime Provider (imperative backends)
	// ---------------------------------------------------------------------
	runtimeProvider := api.NewRuntimeProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("runtime"), r.Recorder)

	// ---------------------------------------------------------------------
	// GitOps Reconciler (Flux integration layer)
	// ---------------------------------------------------------------------
	kustomizer := api.NewKustomizeStrategyProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("reconciliation.kustomize"))

	// ---------------------------------------------------------------------
	// Reconciliation Executor (delivery axis)
	// ---------------------------------------------------------------------
	reconciliationExecutor := api.NewReconciliationExecutor(runtimeProvider, kustomizer, r.Log.WithName("reconciliation"))

	// ---------------------------------------------------------------------
	// Service Layer
	// ---------------------------------------------------------------------
	intentBuilder := application.NewIntentBuilder()
	statusWriter := application.NewStatusWriter(mgr.GetClient(), r.Log.WithName("deployment.status-writer"))
	r.DeploymentService = application.NewDeploymentService(intentBuilder, statusWriter, reconciliationExecutor, ctrl.Log)

	// ---------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	// ---------------------------------------------------------------------
	deployDomain := deploydomain.New(r.DeploymentMediator, r.DeploymentService, cache, r.reader, events, r.Log.WithName("domain.deployment"))
	registry.RegisterDomain(deploymentv1alpha1.GroupVersion.WithKind("Deployment"), deployDomain)

	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&deploymentv1alpha1.Deployment{}).
		Named("environments-deployment").
		WithEventFilter(core.MeaningfulChangePredicate()).
		Complete(r)
}
