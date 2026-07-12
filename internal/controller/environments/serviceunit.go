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

// serviceunit.go reconciles the ServiceUnit CR, routing create/update
// through the core CQRS engine.
//
// SetupWithManager does not yet construct a mediator, provider, or
// service, or call registry.RegisterDomain — see internal/domains/
// serviceunit for where that would plug in once it exists.
package environments

import (
	"context"

	serviceunitv1alpha1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	"github.com/BlanketOps/environments/core"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
)

// ServiceUnitReconciler reconciles a ServiceUnit object
type ServiceUnitReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Runtime  *runtimeinfra.Runtime
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=serviceunits,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=serviceunits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=serviceunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ServiceUnit object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *ServiceUnitReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := ctrl.LoggerFrom(ctx).WithValues("controller", "serviceunit", "namespace", req.Namespace, "name", req.Name)
	ctx = logr.NewContext(ctx, log)
	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch ServiceUnit
	// ------------------------------------------------
	var serviceunit serviceunitv1alpha1.ServiceUnit
	if err := r.Get(ctx, req.NamespacedName, &serviceunit); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: serviceunit not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch serviceunit")
		return ctrl.Result{}, err
	}

	log.Info("serviceunit fetched", "generation", serviceunit.Generation, "resourceVersion", serviceunit.ResourceVersion)

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------
	cmd := core.Command{
		GVK:  serviceunitv1alpha1.GroupVersion.WithKind("ServiceUnit"),
		Type: core.CmdUpdate,
		Obj:  &serviceunit,
	}

	log.Info("routing serviceunit to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {

		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&serviceunit, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		log.Info("reconcile exit: engine error")

		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Persist status (retry-on-conflict)
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest serviceunitv1alpha1.ServiceUnit
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		latest.Status = serviceunit.Status
		return r.Status().Update(ctx, &latest)

	}); err != nil {
		log.Error(err, "failed to update serviceunit status")
		return ctrl.Result{}, err
	}

	log.Info("serviceunit status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *ServiceUnitReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ---------------------------------------------------------------------
	// Logging & events
	// ---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("ServiceUnit")
	r.Recorder = mgr.GetEventRecorder("serviceunit-controller")

	// ---------------------------------------------------------------------
	// Runtime Infrastructure
	// ---------------------------------------------------------------------
	// cache := r.Runtime.Cache
	// events := r.Runtime.Events
	// registry := r.Runtime.Registry

	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&serviceunitv1alpha1.ServiceUnit{}).
		Named("environments-serviceunit").
		WithEventFilter(core.MeaningfulChangePredicate()).
		Complete(r)
}
