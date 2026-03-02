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
	buildtriggerv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BuildTriggerReconciler reconciles a BuildTrigger object
type BuildTriggerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder events.EventRecorder
	Cache    *core.Cache
	Events   *core.EventRecorder
	Registry *core.Registry
	Engine   *core.Engine
}

// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=buildtriggers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=buildtriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=buildtriggers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the BuildTrigger object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *BuildTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := r.Log.WithValues(
		"controller", "buildtrigger",
		"namespace", req.Namespace,
		"name", req.Name,
	)

	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch Build
	// ------------------------------------------------
	var buildtrigger buildtriggerv1alpha1.BuildTrigger
	if err := r.Get(ctx, req.NamespacedName, &buildtrigger); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: buildtrigger not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch buildtrigger")
		return ctrl.Result{}, err
	}

	log.Info(
		"buildtrigger fetched",
		"generation", buildtrigger.Generation,
		"resourceVersion", buildtrigger.ResourceVersion,
	)

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------
	cmd := core.Command{
		GVK:  buildtriggerv1alpha1.GroupVersion.WithKind("BuildTrigger"),
		Type: core.CmdUpdate,
		Obj:  &buildtrigger,
	}

	log.Info(
		"routing buildtrigger to core engine",
		"gvk", cmd.GVK.String(),
		"command", cmd.Type,
	)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")

		r.Recorder.Eventf(
			&buildtrigger, // regarding
			nil,           // related (none)
			corev1.EventTypeWarning,
			"EngineFailure", // reason
			"Execute",       // action (short verb)
			"%v",            // note (format)
			err,             // args
		)

		log.Info("reconcile exit: engine error")
		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Persist status (retry-on-conflict)
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest buildtriggerv1alpha1.BuildTrigger
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		latest.Status = buildtrigger.Status
		return r.Status().Update(ctx, &latest)
	}); err != nil {
		log.Error(err, "failed to update buildtrigger status")
		return ctrl.Result{}, err
	}

	log.Info("buildtrigger status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BuildTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {

	//---------------------------------------------------------------------
	// Logging & events
	//---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("BuildTrigger")
	r.Recorder = mgr.GetEventRecorder("buildtriger-controller")

	//---------------------------------------------------------------------
	// Core infrastructure
	//---------------------------------------------------------------------
	r.Cache = core.NewCache(mgr, nil)
	r.Events = core.NewEventRecorder(r.Recorder)
	r.Registry = core.NewRegistry()
	r.Engine = core.NewEngine(r.Registry, ctrl.Log.WithName("engine"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&buildtriggerv1alpha1.BuildTrigger{}).
		WithEventFilter(core.MeaningfulChangePredicate()).
		Named("environments-buildtrigger").
		Complete(r)
}
