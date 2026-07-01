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
	packagev1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	pkgProvider "github.com/ntlaletsi70/blanketops-environments/pkg/apis/packages/api"
	pkgApp "github.com/ntlaletsi70/blanketops-environments/pkg/apis/packages/application"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pkgDomain "github.com/ntlaletsi70/blanketops-environments-controller/internal/domains/packages"
	pkgMediator "github.com/ntlaletsi70/blanketops-environments-controller/internal/mediators/packages"
	runtimeinfra "github.com/ntlaletsi70/blanketops-environments-controller/internal/runtime"
)

// PackageReconciler reconciles a Package object
type PackageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger

	PackageMediator *pkgMediator.Mediator
	Runtime         *runtimeinfra.Runtime
	Recorder        events.EventRecorder
}

// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=packages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=packages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=packages/finalizers,verbs=update

// +kubebuilder:rbac:groups=packaging.carvel.dev,resources=packages;packageinstalls;packagerepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=packaging.carvel.dev,resources=packageinstalls/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=data.packaging.carvel.dev,resources=packages,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Package object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *PackageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	log := ctrl.LoggerFrom(ctx).WithValues("controller", "package", "namespace", req.Namespace, "name", req.Name)
	ctx = logr.NewContext(ctx, log)
	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch Package
	// ------------------------------------------------
	var packages packagev1alpha1.Package
	if err := r.Get(ctx, req.NamespacedName, &packages); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: package not found (deleted)")
			return ctrl.Result{}, nil
		}

		log.Error(err, "failed to fetch package")
		return ctrl.Result{}, err
	}

	log.Info("package fetched", "generation", packages.Generation, "resourceVersion", packages.ResourceVersion)

	// ------------------------------------------------
	// Construct core command
	// ------------------------------------------------
	cmd := core.Command{
		GVK:  packagev1alpha1.GroupVersion.WithKind("Package"),
		Type: core.CmdUpdate,
		Obj:  &packages,
	}

	log.Info("routing package to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// ------------------------------------------------
	// Execute domain logic via engine
	// ------------------------------------------------
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {

		log.Error(err, "engine execution failed")
		// r.Recorder.Eventf(&packages, nil, corev1.EventTypeWarning, "EngineFailure", "%v", err)
		log.Info("reconcile exit: engine error")

		return ctrl.Result{}, err
	}

	log.Info("engine execution completed")

	// ------------------------------------------------
	// Persist status (retry-on-conflict)
	// ------------------------------------------------
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest packagev1alpha1.Package
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}

		latest.Status = packages.Status
		return r.Status().Update(ctx, &latest)

	}); err != nil {
		log.Error(err, "failed to update package status")
		return ctrl.Result{}, err
	}

	log.Info("package status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// -----------------------------------------------------------------
// SetupWithManager sets up the controller with the Manager.
// -----------------------------------------------------------------
func (r *PackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ---------------------------------------------------------------------
	// Logging & events
	// ---------------------------------------------------------------------
	r.Log = ctrl.Log.WithName("controllers").WithName("Package")
	r.Recorder = mgr.GetEventRecorder("package-controller")

	// ---------------------------------------------------------------------
	// Runtime Infrastructure
	// ---------------------------------------------------------------------
	cache := r.Runtime.Cache
	eventsRecorder := r.Runtime.Events
	registry := r.Runtime.Registry

	// ---------------------------------------------------------------------
	// Mediator (prerequisites only)
	// ---------------------------------------------------------------------
	r.PackageMediator = pkgMediator.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.package"), r.Recorder)

	// ---------------------------------------------------------------------
	// Providers (kapp)
	// ---------------------------------------------------------------------
	kappProvider := pkgProvider.NewApplicationProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.kapp"), r.Recorder)

	// ---------------------------------------------------------------------
	// BackendSelector (Backend selector maps strategy -> provider)
	// ---------------------------------------------------------------------
	backendSelector := pkgApp.NewBackendSelector(kappProvider)

	// -----------------------------------------------------------------------------------------
	// Package Service (Mapper and StatiusWriter, domain service for orchestration))
	// ------------------------------------------------------------------------------------------
	mapper := pkgApp.NewMapper()
	statusWriter := pkgApp.NewStatusWriter(r.Client, r.Log.WithName("package-status-writer"))
	packageService := pkgApp.NewPackageService(mapper, backendSelector, statusWriter)

	// --------------------------------------------------------------------------------
	// Registry ( Domain Registration, domain orchestrates mediator + service)
	// --------------------------------------------------------------------------------
	pkgDomainInst := pkgDomain.New(r.PackageMediator, packageService, cache, eventsRecorder, r.Log.WithName("domain.package"))
	registry.RegisterDomain(packagev1alpha1.GroupVersion.WithKind("Package"), pkgDomainInst)

	// ---------------------------------------------------------------------
	// Controller registration
	// ---------------------------------------------------------------------
	return ctrl.NewControllerManagedBy(mgr).
		For(&packagev1alpha1.Package{}).
		Named("environments-package").
		WithEventFilter(core.MeaningfulChangePredicate()).
		Complete(r)
}
