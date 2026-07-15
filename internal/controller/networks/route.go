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

/*
This file owns RouteReconciler — the controller-runtime reconciler for the
Route CR.

The reconciler is deliberately thin. It owns three responsibilities only:

 1. Fetch the Route CR (NotFound → drop, the CR is gone).
 2. Resolve the raw contract into a ResolvedRoute (resolution layer).
 3. Hand the ResolvedRoute to RouteService, which maps, selects, dispatches,
    and writes status.

All business logic lives in pkg/routes/application. The reconciler does not
build conditions, select providers, or touch the runtime resource directly.
A resolution failure is terminal for this generation — it is logged and the
request is dropped (no requeue) because re-running the same bad contract will
fail identically. Service errors are returned for controller-runtime to requeue
with backoff.
*/
package networks

import (
	"context"

	networksv1alpha1 "github.com/blanketops/environments-api/api/networks/v1alpha1"
	"github.com/blanketops/environments/core"
	"github.com/go-logr/logr"

	// routeapp "github.com/blanketops/environments/pkg/apis/route/application"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	// routedomain "github.com/blanketops/environments-controller/internal/domains/route"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
)

// routeFinalizer gates deletion of a Route CR until CleanupPrerequisites and
// Teardown have both run successfully. See Reconcile for the add/check/
// remove lifecycle.
const routeFinalizer = "networks.blanketops.dev/route-finalizer"

// RouteReconciler reconciles a Route CR by resolving its contract and handing
// it to the route application service.
type RouteReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
	//	RouteService *routeapp.RouteService
	Runtime  *runtimeinfra.Runtime
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=networks.blanketops.dev,resources=routes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networks.blanketops.dev,resources=routes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networks.blanketops.dev,resources=routes/finalizers,verbs=update
// +kubebuilder:rbac:groups=serving.knative.dev,resources=domainmappings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Build object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *RouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// log := log.FromContext(ctx).WithValues("route", req.NamespacedName)

	log := ctrl.LoggerFrom(ctx).WithValues("controller", "route", "namespace", req.Namespace, "name", req.Name)
	ctx = logr.NewContext(ctx, log)

	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch Route
	// ------------------------------------------------
	var routeCR networksv1alpha1.Route
	if err := r.Get(ctx, req.NamespacedName, &routeCR); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: route not found (deleted)")
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to fetch route")
		return ctrl.Result{}, err
	}
	log.Info("route fetched", "generation", routeCR.Generation, "resourceVersion", routeCR.ResourceVersion)

	// ------------------------------------------------
	// Finalizer gate — determines cmd.Type
	// ------------------------------------------------
	cmdType := core.CmdUpdate
	if !routeCR.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&routeCR, routeFinalizer) {
			log.Info("reconcile exit: deletion in progress, finalizer already removed")
			return ctrl.Result{}, nil
		}
		cmdType = core.CmdDelete
	} else if !controllerutil.ContainsFinalizer(&routeCR, routeFinalizer) {
		controllerutil.AddFinalizer(&routeCR, routeFinalizer)
		if err := r.Update(ctx, &routeCR); err != nil {
			log.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer added")
		return ctrl.Result{Requeue: true}, nil
	}

	// -------------------------------------------------
	// Construct core command
	// -------------------------------------------------
	cmd := core.Command{
		GVK:  networksv1alpha1.GroupVersion.WithKind("Route"),
		Type: cmdType,
		Obj:  &routeCR,
	}

	log.Info("routing route to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&routeCR, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
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
	if cmdType == core.CmdDelete {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest networksv1alpha1.Route
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				return client.IgnoreNotFound(err)
			}
			controllerutil.RemoveFinalizer(&latest, routeFinalizer)
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
		var latest networksv1alpha1.Route
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}
		latest.Status = routeCR.Status
		return r.Status().Update(ctx, &latest)
	}); err != nil {
		log.Error(err, "failed to update route status")
		return ctrl.Result{}, err
	}

	log.Info("route status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *RouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ---------------------------------------------------------------------
	// Logging & events
	// ---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("Route")
	r.Recorder = mgr.GetEventRecorder("route-controller")

	// ---------------------------------------------------------------------
	// Runtime Infrastructure
	// ---------------------------------------------------------------------
	// cache := r.Runtime.Cache
	// eventsRecorder := r.Runtime.Events
	// registry := r.Runtime.Registry

	// -----------------------------------------------------------------------------------------
	// Build Service (Mapper and StatiusWriter, domain service for orchestration))
	// ------------------------------------------------------------------------------------------
	// mapper := routeapp.NewMapper()
	// statusWriter := routeapp.NewStatusWriter(r.Client, r.Log.WithName("route-status-writer"))
	// r.RouteService = routeapp.NewRouteService(mapper, statusWriter, backendSelector)

	// --------------------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	// --------------------------------------------------------------------------------
	// routeDomain := routedomain.New(r.BuildMediator, r.BuildService, cache, eventsRecorder, r.Log.WithName("domain.route"))
	// registry.RegisterDomain(networksv1alpha1.GroupVersion.WithKind("Route"), routeDomain)

	return ctrl.NewControllerManagedBy(mgr).
		For(&networksv1alpha1.Route{}).
		Complete(r)
}
