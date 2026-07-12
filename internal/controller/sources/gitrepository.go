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

// gitrepository.go reconciles the GitRepository CR — the entry point of
// the delivery pipeline (GitRepository → GitHubEvent → Build →
// SupplyChain → Package → Deployment). On setup it wires the
// GitRepository domain's mediator, GitHub provider, and service into the
// domain registry.
//
// The finalizer (GitRepositoryFinalizer) gates deletion on teardown of
// cluster-scoped, label-linked resources (the Crossplane Repository and
// RepositoryWebhook) that Kubernetes GC can't reclaim on its own — see
// the constant's doc comment for the full add/check/remove lifecycle.
package sources

import (
	"context"

	sourcesv1alpha1 "github.com/BlanketOps/environments-api/api/sources/v1alpha1"
	"github.com/BlanketOps/environments/core"
	gitrepoapi "github.com/BlanketOps/environments/pkg/apis/gitrepository/api"
	"github.com/BlanketOps/environments/pkg/apis/gitrepository/application"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	gitrepositorydomain "github.com/BlanketOps/environments-controller/internal/domains/gitrepository"
	"github.com/BlanketOps/environments-controller/internal/mediators/gitrepository"
	runtimeinfra "github.com/BlanketOps/environments-controller/internal/runtime"
)

// GitRepositoryFinalizer gates GitRepository deletion on teardown of the
// resources the domain provisioned: the Crossplane Repository and
// RepositoryWebhook (cluster-scoped, label-linked only — Kubernetes GC
// cannot reclaim them) and the per-CR prerequisites. The finalizer is
// removed only after CmdDelete completes successfully; any teardown error
// keeps it in place for retry on the next reconcile.
const GitRepositoryFinalizer = "sources.blanketops.dev/gitrepository-finalizer"

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

// +kubebuilder:rbac:groups=repo.github.upbound.io,resources=repositories;repositorywebhooks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=github.upbound.io,resources=providerconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// The lifecycle is finalizer-gated: live objects get the finalizer added
// before any domain work runs, and deletion routes CmdDelete through the
// engine — the finalizer is removed only after teardown succeeds.
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
	var gitRepositoryCR sourcesv1alpha1.GitRepository
	if err := r.Get(ctx, req.NamespacedName, &gitRepositoryCR); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: gitrepository not found (deleted)")
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to fetch gitrepository")
		return ctrl.Result{}, err
	}
	log.Info("gitrepository fetched", "generation", gitRepositoryCR.Generation, "resourceVersion", gitRepositoryCR.ResourceVersion)
	// ------------------------------------------------
	// Deletion: route CmdDelete, then release finalizer
	// ------------------------------------------------
	if !gitRepositoryCR.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&gitRepositoryCR, GitRepositoryFinalizer) {
			// Nothing gating deletion — let Kubernetes finish.
			log.Info("reconcile exit: deleting without finalizer")
			return ctrl.Result{}, nil
		}
		log.Info("gitrepository deletion requested; routing teardown")
		cmd := core.Command{
			GVK:  sourcesv1alpha1.GroupVersion.WithKind("GitRepository"),
			Type: core.CmdDelete,
			Obj:  &gitRepositoryCR,
		}
		if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {
			// Teardown incomplete — keep the finalizer, retry next reconcile.
			log.Error(err, "teardown failed; finalizer retained")
			r.Recorder.Eventf(&gitRepositoryCR, nil, corev1.EventTypeWarning, "TeardownFailure", "Execute", "%v", err)
			return ctrl.Result{}, err
		}
		log.Info("teardown complete; removing finalizer")
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest sourcesv1alpha1.GitRepository
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				return client.IgnoreNotFound(err)
			}
			if !controllerutil.ContainsFinalizer(&latest, GitRepositoryFinalizer) {
				return nil
			}
			controllerutil.RemoveFinalizer(&latest, GitRepositoryFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
		log.Info("reconcile done: gitrepository released for deletion")
		return ctrl.Result{}, nil
	}
	// ------------------------------------------------
	// Live object: ensure finalizer before any domain work
	// ------------------------------------------------
	if !controllerutil.ContainsFinalizer(&gitRepositoryCR, GitRepositoryFinalizer) {
		log.Info("adding finalizer")
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest sourcesv1alpha1.GitRepository
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				return err
			}
			if controllerutil.ContainsFinalizer(&latest, GitRepositoryFinalizer) {
				return nil
			}
			controllerutil.AddFinalizer(&latest, GitRepositoryFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		// The Update bumps resourceVersion and triggers a fresh reconcile;
		// exit here and let that reconcile run the domain against the
		// finalized object.
		log.Info("reconcile done: finalizer added, requeue via watch")
		return ctrl.Result{}, nil
	}
	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------
	cmd := core.Command{
		GVK:  sourcesv1alpha1.GroupVersion.WithKind("GitRepository"),
		Type: core.CmdUpdate,
		Obj:  &gitRepositoryCR,
	}
	log.Info("routing gitrepository to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)
	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&gitRepositoryCR, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
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
		latest.Status = gitRepositoryCR.Status
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
	eventsRecorder := r.Runtime.Events
	registry := r.Runtime.Registry
	// ---------------------------------------------------------------------
	// Mediator (prerequisites only)
	// ---------------------------------------------------------------------
	r.GitRepositoryMediator = gitrepository.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.gitrepository"), r.Recorder)
	// ---------------------------------------------------------------------
	// Providers (github)
	// ---------------------------------------------------------------------
	githubProvider := gitrepoapi.NewGitHubProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.github"), r.Recorder)
	// ---------------------------------------------------------------------
	// BackendSelector (Backend selector maps strategy -> provider)
	// ---------------------------------------------------------------------
	backendSelector := application.NewBackendSelector(githubProvider)
	// -----------------------------------------------------------------------------------------
	// GitRepository Service (Mapper and StatusWriter, domain service for orchestration)
	// ------------------------------------------------------------------------------------------
	mapper := application.NewMapper()
	statusWriter := application.NewStatusWriter()
	r.GitRepositoryService = application.NewGitRepositoryService(mapper, statusWriter, backendSelector)
	// --------------------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	// --------------------------------------------------------------------------------
	gitRepoDomainInst := gitrepositorydomain.New(r.GitRepositoryMediator, r.GitRepositoryService, cache, eventsRecorder, r.Log.WithName("domain.gitrepository"))
	registry.RegisterDomain(sourcesv1alpha1.GroupVersion.WithKind("GitRepository"), gitRepoDomainInst)
	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&sourcesv1alpha1.GitRepository{}).
		Named("sources-gitrepository").
		WithEventFilter(core.MeaningfulChangePredicate()).
		Complete(r)
}
