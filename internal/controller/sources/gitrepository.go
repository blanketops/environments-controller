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

package sources

import (
	"context"

	"github.com/go-logr/logr"
	sourcesv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/sources/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	gitrepoapi "github.com/ntlaletsi70/blanketops-environments/pkg/gitrepository/api"
	"github.com/ntlaletsi70/blanketops-environments/pkg/gitrepository/application"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gitrepositorydomain "github.com/ntlaletsi70/blanketops-environments-controller/internal/domains/gitrepository"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/gitrepository"
	runtimeinfra "github.com/ntlaletsi70/blanketops-environments-controller/internal/runtime"
)

// GitRepositoryReconciler reconciles a GitRepository object
type GitRepositoryReconciler struct {
	client.Client
	GitRepositoryService  *application.GitRepositoryService
	Scheme                *runtime.Scheme
	GitRepositoryMediator *gitrepository.Mediator
	Log                   logr.Logger
	Runtime               *runtimeinfra.Runtime
	Recorder              events.EventRecorder
}

// +kubebuilder:rbac:groups=sources.blanketops.dev,resources=gitrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sources.blanketops.dev,resources=gitrepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sources.blanketops.dev,resources=gitrepositories/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the GitRepository object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *GitRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := ctrl.LoggerFrom(ctx).WithValues("controller", "gitrepository", "namespace", req.Namespace, "name", req.Name)
	ctx = logr.NewContext(ctx, log)

	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch GitRepository
	// ------------------------------------------------
	var gitrepository sourcesv1alpha1.GitRepository
	if err := r.Get(ctx, req.NamespacedName, &gitrepository); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: gitrepository not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch gitrepository")
		return ctrl.Result{}, err
	}

	log.Info("gitrepository fetched", "generation", gitrepository.Generation, "resourceVersion", gitrepository.ResourceVersion)

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------
	cmd := core.Command{
		GVK:  sourcesv1alpha1.GroupVersion.WithKind("GitRepository"),
		Type: core.CmdUpdate,
		Obj:  &gitrepository,
	}

	log.Info("routing gitrepository to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&gitrepository, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		log.Info("reconcile exit: engine error")
		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Persist status (retry-on-conflict)
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest sourcesv1alpha1.GitRepository
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		latest.Status = gitrepository.Status
		return r.Status().Update(ctx, &latest)

	}); err != nil {
		log.Error(err, "failed to update gitrepository status")
		return ctrl.Result{}, err
	}

	log.Info("gitrepository status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *GitRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ---------------------------------------------------------------------
	// Logging & events
	// ---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("GitRepository")
	r.Recorder = mgr.GetEventRecorder("gitrepository-controller")

	// ---------------------------------------------------------------------
	// Runtime Infrastructure
	// ---------------------------------------------------------------------
	cache := r.Runtime.Cache
	events := r.Runtime.Events
	registry := r.Runtime.Registry

	// ---------------------------------------------------------------------
	// Mediator (prerequisites only)
	// ---------------------------------------------------------------------
	r.GitRepositoryMediator = gitrepository.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.gitrepository"), r.Recorder)

	// ---------------------------------------------------------------------
	// Providers (strategy handlers)
	// ---------------------------------------------------------------------

	// ---------------------------------------------------------------------
	// Providers (github)
	// ---------------------------------------------------------------------
	githubProvider := gitrepoapi.NewGitHubProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.github"), r.Recorder)

	// ---------------------------------------------------------------------
	// BackendSelector (Backend selector maps strategy -> provider)
	// ---------------------------------------------------------------------
	backendSelector := application.NewBackendSelector(githubProvider)

	// -----------------------------------------------------------------------------------------
	// GitRepository Service (Mapper and StatiusWriter, domain service for orchestration))
	// ------------------------------------------------------------------------------------------
	mapper := application.NewMapper()
	statusWriter := application.NewStatusWriter() //check args for a fix here please, extra argument required
	r.GitRepositoryService = application.NewGitRepositoryService(mapper, statusWriter, backendSelector)

	// --------------------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	// --------------------------------------------------------------------------------
	gitrepositorydomain := gitrepositorydomain.New(r.GitRepositoryMediator, r.GitRepositoryService, cache, events, r.Log.WithName("domain.gitrepository"))
	registry.RegisterDomain(sourcesv1alpha1.GroupVersion.WithKind("GitRepository"), gitrepositorydomain)

	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&sourcesv1alpha1.GitRepository{}).
		Named("sources-gitrepository").
		WithEventFilter(core.MeaningfulChangePredicate()).
		Complete(r)
}
