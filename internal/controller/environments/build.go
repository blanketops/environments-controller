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

	"github.com/go-logr/logr"
	buildv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	buildapi "github.com/ntlaletsi70/blanketops-environments/pkg/build/api"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	buildapp "github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	buildclientset "github.com/shipwright-io/build/pkg/client/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/mediators/build"
	builddomain "github.com/ntlaletsi70/blanketops-environments-controller/internal/domains/build"
	runtimeinfra "github.com/ntlaletsi70/blanketops-environments-controller/internal/runtime"
)

// BuildReconciler reconciles a Build object
type BuildReconciler struct {
	client.Client
	KubeClient    kubernetes.Interface
	BuildClient   buildclientset.Interface
	BuildService  *application.BuildService
	Scheme        *runtime.Scheme
	BuildMediator *build.Mediator
	Log           logr.Logger
	Runtime       *runtimeinfra.Runtime
	Recorder      events.EventRecorder
}

// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=builds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=builds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=builds/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Build object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *BuildReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := r.Log.WithValues("controller", "build", "namespace", req.Namespace, "name", req.Name)
	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch Build
	// ------------------------------------------------
	var build buildv1alpha1.Build
	if err := r.Get(ctx, req.NamespacedName, &build); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: build not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch build")
		return ctrl.Result{}, err
	}

	log.Info("build fetched", "generation", build.Generation, "resourceVersion", build.ResourceVersion)

	//-------------------------------------------------
	// Construct core command
	//-------------------------------------------------
	cmd := core.Command{
		GVK:  buildv1alpha1.GroupVersion.WithKind("Build"),
		Type: core.CmdUpdate,
		Obj:  &build,
	}

	log.Info("routing build to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {

		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&build, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		log.Info("reconcile exit: engine error")

		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Persist status (retry-on-conflict)
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest buildv1alpha1.Build
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		latest.Status = build.Status
		return r.Status().Update(ctx, &latest)

	}); err != nil {
		log.Error(err, "failed to update build status")
		return ctrl.Result{}, err
	}

	log.Info("build status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *BuildReconciler) SetupWithManager(mgr ctrl.Manager) error {
	//---------------------------------------------------------------------
	// Logging & events
	//---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("Build")
	r.Recorder = mgr.GetEventRecorder("build-controller")

	//---------------------------------------------------------------------
	// Runtime Infrastructure
	//---------------------------------------------------------------------
	cache := r.Runtime.Cache
	events := r.Runtime.Events
	registry := r.Runtime.Registry

	//---------------------------------------------------------------------
	// Mediator (prerequisites only)
	//---------------------------------------------------------------------
	r.BuildMediator = build.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.build"), r.Recorder)

	//---------------------------------------------------------------------
	// Providers (strategy handlers)
	//---------------------------------------------------------------------

	//---------------------------------------------------------------------
	// Providers (buildah)
	//---------------------------------------------------------------------
	buildahProvider := buildapi.NewBuildahProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.buildah"), r.Recorder)

	//---------------------------------------------------------------------
	// Providers (kaniko)
	//---------------------------------------------------------------------
	kanikoProvider := buildapi.NewKanikoProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.kaniko"), r.Recorder)

	//---------------------------------------------------------------------
	// Providers (buildpacks)
	//---------------------------------------------------------------------
	buildpacksProvider := buildapi.NewBuildpacksProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.buildpacks"), r.Recorder)

	//---------------------------------------------------------------------
	// BackendSelector (Backend selector maps strategy -> provider)
	//---------------------------------------------------------------------
	backendSelector := buildapp.NewBackendSelector(buildahProvider, kanikoProvider, buildpacksProvider)

	//-----------------------------------------------------------------------------------------
	// Build Service (Mapper and StatiusWriter, domain service for orchestration))
	//------------------------------------------------------------------------------------------
	mapper := buildapp.NewMapper()
	statusWriter := buildapp.NewStatusWriter(r.Client, r.Log.WithName("build-status-writer"))
	r.BuildService = buildapp.NewBuildService(mapper, statusWriter, backendSelector)

	//--------------------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	//--------------------------------------------------------------------------------
	buildDomain := builddomain.New(r.BuildMediator, r.BuildService, cache, events, r.Log.WithName("domain.build"))
	registry.RegisterDomain(buildv1alpha1.GroupVersion.WithKind("Build"), buildDomain)

	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&buildv1alpha1.Build{}).
		Watches(
			&shipwrightv1alpha1.BuildRun{},
			handler.EnqueueRequestForOwner(
				mgr.GetScheme(),
				mgr.GetRESTMapper(),
				&shipwrightv1alpha1.BuildRun{},
			),
		).
		WithEventFilter(core.MeaningfulChangePredicate()).
		Named("environments-build").
		Complete(r)
}
