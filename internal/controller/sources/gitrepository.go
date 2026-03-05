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

	sourcesv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/sources/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GitRepositoryReconciler reconciles a GitRepository object
type GitRepositoryReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Recorder events.EventRecorder
	Cache    *core.Cache
	Events   *core.EventRecorder
	Registry *core.Registry
	Engine   *core.Engine
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
	log := r.Log.WithValues(
		"controller", "gitrepository",
		"namespace", req.Namespace,
		"name", req.Name,
	)

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

	log.Info(
		"gitrepository fetched",
		"generation", gitrepository.Generation,
		"resourceVersion", gitrepository.ResourceVersion,
	)

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------
	cmd := core.Command{
		GVK:  sourcesv1alpha1.GroupVersion.WithKind("GitRepository"),
		Type: core.CmdUpdate,
		Obj:  &gitrepository,
	}

	log.Info(
		"routing gitrepository to core engine",
		"gvk", cmd.GVK.String(),
		"command", cmd.Type,
	)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")

		r.Recorder.Eventf(
			&gitrepository, // regarding
			nil,            // related (none)
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

// SetupWithManager sets up the controller with the Manager.
func (r *GitRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {

	//---------------------------------------------------------------------
	// Logging & events
	//---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("GitRepository")
	r.Recorder = mgr.GetEventRecorder("gitrepository-controller")

	//---------------------------------------------------------------------
	// Core infrastructure
	//---------------------------------------------------------------------
	r.Cache = core.NewCache(mgr, nil)
	r.Events = core.NewEventRecorder(r.Recorder)
	r.Registry = core.NewRegistry()
	r.Engine = core.NewEngine(r.Registry, ctrl.Log.WithName("engine"))

	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		// For().
		Named("sources-gitrepository").
		Complete(r)
}
