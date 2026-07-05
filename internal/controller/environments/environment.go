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
package environments

import (
	"context"

	environmentv1alpha1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/ntlaletsi70/blanketops-environments/core"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	environmentdomain "github.com/ntlaletsi70/blanketops-environments-controller/internal/domains/environment"
	runtimeinfra "github.com/ntlaletsi70/blanketops-environments-controller/internal/runtime"
)

// EnvironmentReconciler reconciles an Environment object.
type EnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	Runtime  *runtimeinfra.Runtime
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=environments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=environments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=environments/finalizers,verbs=update
// +kubebuilder:rbac:groups=external-secrets.io,resources=clustersecretstores,verbs=get;list;watch
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"controller", "environment",
		"namespace", req.Namespace,
		"name", req.Name,
	)
	ctx = logr.NewContext(ctx, log)
	log.Info("reconcile start")

	// ── Fetch ─────────────────────────────────────────────────────────────────
	var environment environmentv1alpha1.Environment
	if err := r.Get(ctx, req.NamespacedName, &environment); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: environment not found (deleted)")
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to fetch environment")
		return ctrl.Result{}, err
	}

	log.Info("environment fetched",
		"generation", environment.Generation,
		"resourceVersion", environment.ResourceVersion,
	)

	// ── Route to engine ───────────────────────────────────────────────────────
	cmd := core.Command{
		GVK:  environmentv1alpha1.GroupVersion.WithKind("Environment"),
		Type: core.CmdUpdate,
		Obj:  &environment,
	}

	log.Info("routing environment to core engine",
		"gvk", cmd.GVK.String(),
		"command", cmd.Type,
	)

	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&environment, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ── Persist status ────────────────────────────────────────────────────────
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest environmentv1alpha1.Environment
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}
		latest.Status = environment.Status
		return r.Status().Update(ctx, &latest)
	}); err != nil {
		log.Error(err, "failed to update environment status")
		return ctrl.Result{}, err
	}

	log.Info("reconcile done")
	return ctrl.Result{}, nil
}

func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ── Logging & events ──────────────────────────────────────────────────────
	r.Log = ctrl.Log.WithName("controllers").WithName("Environment")
	r.Recorder = mgr.GetEventRecorder("environment-controller")

	// ── Runtime infrastructure ────────────────────────────────────────────────
	cache := r.Runtime.Cache
	evts := r.Runtime.Events
	registry := r.Runtime.Registry

	// ── Domain ────────────────────────────────────────────────────────────────
	// Environment domain is intentionally minimal at this stage:
	// - Resolves the environment contract
	// - Validates secretStore provider is declared
	// - Secret store connection testing deferred (next phase)
	// - Observer (ref patcher) deferred (next phase)
	environmentDomain := environmentdomain.New(
		mgr.GetClient(),
		mgr.GetScheme(),
		cache,
		evts,
		r.Log.WithName("domain.environment"),
	)
	registry.RegisterDomain(
		environmentv1alpha1.GroupVersion.WithKind("Environment"),
		environmentDomain,
	)

	// ── Controller registration ───────────────────────────────────────────────
	return ctrl.NewControllerManagedBy(mgr).
		For(&environmentv1alpha1.Environment{}).
		Named("environments").
		WithEventFilter(core.MeaningfulChangePredicate()).
		Complete(r)
}
