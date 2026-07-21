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

// deployment.go reconciles the Deployment CR: routes create/update/delete
// through the core CQRS engine and, on setup, wires the Deployment
// domain's mediator, runtime provider, Kustomize reconciliation executor,
// and service into the domain registry.
//
// The finalizer (deploymentFinalizer) gates deletion until the engine's
// CmdDelete path completes, mirroring build.go.
package environments

import (
	"context"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/predicates"
	"github.com/blanketops/environments/pkg/apis/deployment/api"
	"github.com/blanketops/environments/pkg/apis/deployment/application"
	"github.com/blanketops/environments/pkg/apis/deployment/reconcile"
	"github.com/blanketops/environments/pkg/apis/deployment/strategy"
	deploymentintent "github.com/blanketops/environments/pkg/intent/deployment"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	deploydomain "github.com/blanketops/environments-controller/internal/domains/deployment"
	deployment "github.com/blanketops/environments-controller/internal/mediators/deployment"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
)

// deploymentFinalizer gates deletion of a Deployment CR until CleanupPrerequisites and
// Teardown have both run successfully. See Reconcile for the add/check/
// remove lifecycle.
const deploymentFinalizer = "environments.blanketops.dev/deployment-finalizer"

// DeploymentReconciler reconciles a Deployment object
type DeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
	// reader serves cross-CR reads (e.g. fetching a ServiceUnit by name)
	// that must bypass the projection cache, which is not used for
	// correctness-bearing lookups.
	reader             client.Reader
	Recorder           events.EventRecorder
	Runtime            *runtimeinfra.Runtime
	DeploymentMediator *deployment.Mediator
	DeploymentService  *application.DeploymentService
}

// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch

// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories;helmrepositories;ocirepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kappctrl.k14s.io,resources=apps,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=gitrepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch;create;update;patch;delete

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
	var deploymentCR environmentsv1alpha1.Deployment
	if err := r.Get(ctx, req.NamespacedName, &deploymentCR); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: deployment not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch deployment")
		return ctrl.Result{}, err
	}

	log.Info("deployment fetched", "generation", deploymentCR.Generation, "resourceVersion", deploymentCR.ResourceVersion)
	// ------------------------------------------------
	// Finalizer gate — determines cmd.Type
	// ------------------------------------------------
	cmdType := command.CmdUpdate
	if !deploymentCR.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&deploymentCR, deploymentFinalizer) {
			log.Info("reconcile exit: deletion in progress, finalizer already removed")
			return ctrl.Result{}, nil
		}
		cmdType = command.CmdDelete
	} else if !controllerutil.ContainsFinalizer(&deploymentCR, deploymentFinalizer) {
		controllerutil.AddFinalizer(&deploymentCR, deploymentFinalizer)
		if err := r.Update(ctx, &deploymentCR); err != nil {
			log.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer added")
		return ctrl.Result{Requeue: true}, nil
	}

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------

	cmd := command.Command{
		GVK:  environmentsv1alpha1.GroupVersion.WithKind("Deployment"),
		Type: cmdType,
		Obj:  &deploymentCR,
	}

	log.Info("routing deployment to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&deploymentCR, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		log.Info("reconcile exit: engine error")
		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Deletion path: remove finalizer now that the engine returned nil.
	// Status is intentionally NOT written here — the object is about to be
	// removed, and racing a status update against finalizer removal serves
	// no purpose.
	// ------------------------------------------------
	if cmdType == command.CmdDelete {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest environmentsv1alpha1.Deployment
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				return client.IgnoreNotFound(err)
			}
			controllerutil.RemoveFinalizer(&latest, deploymentFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer removed, deletion will proceed")
		return ctrl.Result{}, nil
	}
	// ------------------------------------------------
	// Persist status (retry-on-conflict) — create/update path only
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest environmentsv1alpha1.Deployment
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}
		latest.Status = deploymentCR.Status
		return r.Status().Update(ctx, &latest)
	}); err != nil {
		log.Error(err, "failed to update deployment status")
		return ctrl.Result{}, err
	}

	log.Info("deployment status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ---------------------------------------------------------------------
	// Logging & events
	// ---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("Deployment")
	r.Recorder = mgr.GetEventRecorder("deployment-controller")

	// ---------------------------------------------------------------------
	// Runtime Infrastructure
	// ---------------------------------------------------------------------
	cache := r.Runtime.Cache
	eventsRecorder := r.Runtime.Events
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
	runtimeProvider := strategy.NewRuntimeProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("runtime"), r.Recorder)

	// ---------------------------------------------------------------------
	// GitOps Reconciler (Flux integration layer)
	// ---------------------------------------------------------------------
	kustomizer := api.NewKustomizeStrategyProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("reconciliation.kustomize"))

	// ---------------------------------------------------------------------
	// Reconciliation Executor (delivery axis)
	// ---------------------------------------------------------------------
	reconciliationExecutor := reconcile.NewReconciliationExecutor(runtimeProvider, kustomizer, r.Log.WithName("reconciliation"))

	// ---------------------------------------------------------------------
	// Service Layer
	// ---------------------------------------------------------------------
	intentBuilder := deploymentintent.NewIntentBuilder()
	statusWriter := application.NewStatusWriter(mgr.GetClient(), r.Log.WithName("deployment.status-writer"))
	r.DeploymentService = application.NewDeploymentService(intentBuilder, statusWriter, reconciliationExecutor, ctrl.Log)

	// ---------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	// ---------------------------------------------------------------------
	deployDomain := deploydomain.New(r.DeploymentMediator, r.DeploymentService, cache, r.reader, eventsRecorder, r.Log.WithName("domain.deployment"))
	registry.RegisterDomain(environmentsv1alpha1.SchemeBuilder.GroupVersion.WithKind("Deployment"), deployDomain)

	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&environmentsv1alpha1.Deployment{}).
		Named("environments-deployment").
		WithEventFilter(predicates.MeaningfulChangePredicate()).
		Complete(r)
}
