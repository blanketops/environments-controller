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

package events

import (
	"context"

	//eventsv1alpha1 "k8s.io/api/events/v1alpha1"
	argoeventsv1alpha1 "github.com/argoproj/argo-events/pkg/apis/events/v1alpha1"
	"github.com/go-logr/logr"
	eventsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/events/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	githubeventapi "github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/api"
	"github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/application"
	githubeventapp "github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/application"

	//githubeventapp "github.com/ntlaletsi70/blanketops-environments/pkg/githubevent/application"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	githubeventdomain "github.com/ntlaletsi70/blanketops-environments-controller/internal/domains/githubevent"
	githubevent "github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/githubevent"
	runtimeinfra "github.com/ntlaletsi70/blanketops-environments-controller/internal/runtime"
)

// GitHubEventReconciler reconciles a GitHubEvent object
type GitHubEventReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Log                logr.Logger
	Runtime            *runtimeinfra.Runtime
	GitHubEventService *application.GitHubEventService

	Recorder            events.EventRecorder
	GitHubEventMediator *githubevent.Mediator
}

// +kubebuilder:rbac:groups=events.k8s.io,resources=githubevents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=githubevents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=githubevents/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the GitHubEvent object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *GitHubEventReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := r.Log.WithValues("controller", "githubevent", "namespace", req.Namespace, "name", req.Name)
	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch GitHubEvent
	// ------------------------------------------------
	var githubevent eventsv1alpha1.GitHubEvent
	if err := r.Get(ctx, req.NamespacedName, &githubevent); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: githubevent not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch githubevent")
		return ctrl.Result{}, err
	}

	log.Info("githubevent fetched", "generation", githubevent.Generation, "resourceVersion", githubevent.ResourceVersion)

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------
	cmd := core.Command{
		GVK:  eventsv1alpha1.GroupVersion.WithKind("GitHubEvent"),
		Type: core.CmdUpdate,
		Obj:  &githubevent,
	}

	log.Info("routing githubevent to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {

		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&githubevent, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		log.Info("reconcile exit: engine error")

		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Persist status (retry-on-conflict)
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest eventsv1alpha1.GitHubEvent
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		latest.Status = githubevent.Status
		return r.Status().Update(ctx, &latest)

	}); err != nil {
		log.Error(err, "failed to update githubevent status")
		return ctrl.Result{}, err
	}

	log.Info("githubevent status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *GitHubEventReconciler) SetupWithManager(mgr ctrl.Manager) error {
	//---------------------------------------------------------------------
	// Logging & events
	//---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("GitHubEvent")
	r.Recorder = mgr.GetEventRecorder("githubevent-controller")

	//---------------------------------------------------------------------
	// Runtime Infrastructure
	//---------------------------------------------------------------------
	cache := r.Runtime.Cache
	registry := r.Runtime.Registry

	//---------------------------------------------------------------------
	// Mediator (prerequisites only)
	//---------------------------------------------------------------------
	r.GitHubEventMediator = githubevent.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.githubevent"), r.Recorder)

	//---------------------------------------------------------------------
	// Providers (strategy handlers)
	//---------------------------------------------------------------------

	//---------------------------------------------------------------------
	// Providers (github)
	//---------------------------------------------------------------------
	githubProvider := githubeventapi.NewGitHubProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.github"), r.Recorder)

	//---------------------------------------------------------------------
	// BackendSelector (Backend selector maps strategy -> provider)
	//---------------------------------------------------------------------
	backendSelector := githubeventapp.NewBackendSelector(githubProvider)

	//-----------------------------------------------------------------------------------------
	// GitHubEvent Service (Mapper and StatiusWriter, domain service for orchestration))
	//-----------------------------------------------------------------------
	mapper := githubeventapp.NewMapper()
	statusWriter := githubeventapp.NewStatusWriter(r.Client, r.Log.WithName("githubevent-status-writer"))
	r.GitHubEventService = githubeventapp.NewGitHubEventService(mapper, statusWriter, backendSelector)

	//--------------------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	//--------------------------------------------------------------------------------
	eventsDomain := githubeventdomain.New(r.GitHubEventService, r.GitHubEventMediator, r.Runtime.Events, cache, r.Log.WithName("domain.githubevent"))
	registry.RegisterDomain(eventsv1alpha1.GroupVersion.WithKind("GitHubEvent"), eventsDomain)

	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&eventsv1alpha1.GitHubEvent{}).Watches(
		&argoeventsv1alpha1.Sensor{},
		handler.EnqueueRequestForOwner(
			mgr.GetScheme(),
			mgr.GetRESTMapper(),
			&argoeventsv1alpha1.Sensor{},
		),
	).
		Named("events-githubevent").
		WithEventFilter(core.MeaningfulChangePredicate()).
		Complete(r)
}
