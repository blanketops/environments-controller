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

// githubevent.go reconciles the GitHubEvent CR: routes create/update/
// delete through the core CQRS engine and, on setup, wires the
// GitHubEvent domain's mediator, GitHub provider, and service into the
// domain registry. It also watches owned Argo Events Sensors so Sensor
// status changes trigger a re-reconcile of the owning GitHubEvent.
//
// The finalizer (githuEventFinalizer) gates deletion until
// CleanupPrerequisites and Teardown have both run successfully.
package events

import (
	"context"
	"time"

	argoeventsv1alpha1 "github.com/argoproj/argo-events/pkg/apis/events/v1alpha1"
	eventsv1alpha1 "github.com/blanketops/environments-api/api/events/v1alpha1"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/predicates"
	githubeventapi "github.com/blanketops/environments/pkg/apis/githubevent/api"
	githubeventapp "github.com/blanketops/environments/pkg/apis/githubevent/application"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	githubeventdomain "github.com/blanketops/environments-controller/internal/domains/githubevent"
	githubevent "github.com/blanketops/environments-controller/internal/mediators/githubevent"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
)

// githuEventFinalizer gates deletion of a GitHubEvent CR until CleanupPrerequisites and
// Teardown have both run successfully. See Reconcile for the add/check/
// remove lifecycle.
const githuEventFinalizer = "events.blanketops.dev/githubevent-finalizer"

// GitHubEventReconciler reconciles a GitHubEvent object
type GitHubEventReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Log                logr.Logger
	Runtime            *runtimeinfra.Runtime
	GitHubEventService *githubeventapp.GitHubEventService

	Recorder            events.EventRecorder
	GitHubEventMediator *githubevent.Mediator
}

// +kubebuilder:rbac:groups=events.blanketops.dev,resources=githubevents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.blanketops.dev,resources=githubevents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.blanketops.dev,resources=githubevents/finalizers,verbs=update

// +kubebuilder:rbac:groups=argoproj.io,resources=sensors;eventsources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=sensors/status;eventsources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=sensors/finalizers;eventsources/finalizers,verbs=update
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch

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

	// Fetch GitHubEvent
	var gitHubEventCR eventsv1alpha1.GitHubEvent
	if err := r.Get(ctx, req.NamespacedName, &gitHubEventCR); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: githubevent not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch githubevent")
		return ctrl.Result{}, err
	}

	log.Info("githubevent fetched", "generation", gitHubEventCR.Generation, "resourceVersion", gitHubEventCR.ResourceVersion)

	// Finalizer gate — determines cmd.Type
	cmdType := command.CmdUpdate
	if !gitHubEventCR.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&gitHubEventCR, githuEventFinalizer) {
			log.Info("reconcile exit: deletion in progress, finalizer already removed")
			return ctrl.Result{}, nil
		}
		cmdType = command.CmdDelete
	} else if !controllerutil.ContainsFinalizer(&gitHubEventCR, githuEventFinalizer) {
		controllerutil.AddFinalizer(&gitHubEventCR, githuEventFinalizer)
		if err := r.Update(ctx, &gitHubEventCR); err != nil {
			log.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer added")
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Construct core command
	cmd := command.Command{
		GVK:  eventsv1alpha1.GroupVersion.WithKind("GitHubEvent"),
		Type: command.CmdUpdate,
		Obj:  &gitHubEventCR,
	}

	log.Info("routing githubevent to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// Execute domain logic via engine
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {

		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&gitHubEventCR, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		log.Info("reconcile exit: engine error")

		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")
	// Deletion path: remove finalizer now that the engine returned nil.
	// Status is intentionally NOT written here — the object is about to be
	// removed, and racing a status update against finalizer removal serves
	// no purpose.
	if cmdType == command.CmdDelete {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest eventsv1alpha1.GitHubEvent
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				return client.IgnoreNotFound(err)
			}
			controllerutil.RemoveFinalizer(&latest, githuEventFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer removed, deletion will proceed")
		return ctrl.Result{}, nil
	}
	// Persist status (retry-on-conflict)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest eventsv1alpha1.GitHubEvent
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		latest.Status = gitHubEventCR.Status
		return r.Status().Update(ctx, &latest)

	}); err != nil {
		log.Error(err, "failed to update githubevent status")
		return ctrl.Result{}, err
	}

	log.Info("githubevent status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitHubEventReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Logging & events
	r.Log = ctrl.Log.WithName("controllers").WithName("GitHubEvent")
	r.Recorder = mgr.GetEventRecorder("githubevent-controller")

	// Runtime Infrastructure
	cache := r.Runtime.Cache
	registry := r.Runtime.Registry

	// Mediator (prerequisites only)
	r.GitHubEventMediator = githubevent.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.githubevent"), r.Recorder)

	// Providers (strategy handlers)

	// Providers (github)
	githubProvider := githubeventapi.NewGitHubProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.github"), r.Recorder)

	// BackendSelector (Backend selector maps strategy -> provider)
	backendSelector := githubeventapp.NewBackendSelector(githubProvider)

	// GitHubEvent Service (Mapper and StatiusWriter, domain service for orchestration))
	mapper := githubeventapp.NewMapper()
	statusWriter := githubeventapp.NewStatusWriter(r.Client, r.Log.WithName("githubevent-status-writer"))
	r.GitHubEventService = githubeventapp.NewGitHubEventService(mapper, statusWriter, backendSelector)

	// Registry ( Domain Registration, domain orchestrates mediator + service)
	eventsDomain := githubeventdomain.New(r.GitHubEventService, r.GitHubEventMediator, r.Runtime.Events, cache, r.Log.WithName("domain.githubevent"))
	registry.RegisterDomain(eventsv1alpha1.GroupVersion.WithKind("GitHubEvent"), eventsDomain)

	// Controller registration
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
		WithEventFilter(predicates.MeaningfulChangePredicate()).
		Complete(r)
}
