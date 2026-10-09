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

// package.go reconciles the Package CR: routes create/update through the
// core CQRS engine and, on setup, wires the Package domain's mediator,
// kapp-controller provider, and service into the domain registry.
//
// Like build.go and deployment.go, deletion is gated by a finalizer: the
// domain tears the Package down before the finalizer is removed.
package environments

import (
	"context"
	"time"

	packagev1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/predicates"
	pkgProvider "github.com/blanketops/environments/pkg/apis/packages/api"
	pkgApp "github.com/blanketops/environments/pkg/apis/packages/application"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	pkgDomain "github.com/blanketops/environments-controller/internal/domains/packages"
	pkgMediator "github.com/blanketops/environments-controller/internal/mediators/packages"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
)

// packageFinalizer gates deletion of a Package CR until the kapp App and the
// prerequisites it provisioned have been torn down.
const packageFinalizer = "environments.blanketops.dev/package-finalizer"

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
// +kubebuilder:rbac:groups=external-secrets.io,resources=externalsecrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,resourceNames=blanketops-environments-package-deployer-role,verbs=bind

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

	// Fetch Package
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

	// Finalizer gate — determines cmd.Type
	cmdType := command.CmdUpdate
	if !packages.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&packages, packageFinalizer) {
			log.Info("reconcile exit: deletion in progress, finalizer already removed")
			return ctrl.Result{}, nil
		}
		cmdType = command.CmdDelete
	} else if !controllerutil.ContainsFinalizer(&packages, packageFinalizer) {
		controllerutil.AddFinalizer(&packages, packageFinalizer)
		if err := r.Update(ctx, &packages); err != nil {
			log.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer added")
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Construct core command
	cmd := command.Command{
		GVK:  packagev1alpha1.GroupVersion.WithKind("Package"),
		Type: cmdType,
		Obj:  &packages,
	}

	log.Info("routing package to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	before := append([]metav1.Condition(nil), packages.Status.Conditions...)

	// Execute domain logic via engine
	execErr := r.Runtime.Engine.Execute(ctx, cmd)
	if execErr != nil {
		log.Error(execErr, "engine execution failed")
		r.Recorder.Eventf(&packages, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", execErr)
	} else {
		log.Info("engine execution completed")
	}

	// Deletion path: remove the finalizer once teardown returned nil. Status
	// is not written — the object is about to be removed.
	if cmdType == command.CmdDelete && execErr == nil {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest packagev1alpha1.Package
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				return client.IgnoreNotFound(err)
			}
			controllerutil.RemoveFinalizer(&latest, packageFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer removed, deletion will proceed")
		return ctrl.Result{}, nil
	}

	// Persist the conditions this pass set, on success and on failure: a
	// Package that could not be reconciled must say why. Only those
	// conditions are merged; replacing the whole status would discard what
	// the package service wrote since the object was read.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest packagev1alpha1.Package
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}
		for _, cond := range changedConditions(before, packages.Status.Conditions) {
			apimeta.SetStatusCondition(&latest.Status.Conditions, cond)
		}
		return r.Status().Update(ctx, &latest)
	}); err != nil {
		log.Error(err, "failed to update package status")
		if execErr == nil {
			return ctrl.Result{}, err
		}
	}

	if execErr != nil {
		log.Info("reconcile exit: engine error")
		return ctrl.Result{}, execErr
	}

	log.Info("package status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PackageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Logging & events
	r.Log = ctrl.Log.WithName("controllers").WithName("Package")
	r.Recorder = mgr.GetEventRecorder("package-controller")

	// Runtime Infrastructure
	cache := r.Runtime.Cache
	eventsRecorder := r.Runtime.Events
	registry := r.Runtime.Registry

	// Mediator (prerequisites only)
	r.PackageMediator = pkgMediator.New(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("mediator.package"), r.Recorder)

	// Providers (kapp)
	kappProvider := pkgProvider.NewApplicationProvider(mgr.GetClient(), mgr.GetScheme(), r.Log.WithName("provider.kapp"), r.Recorder)

	// BackendSelector (Backend selector maps strategy -> provider)
	backendSelector := pkgApp.NewBackendSelector(kappProvider)

	// Package Service (Mapper and StatiusWriter, domain service for orchestration))
	mapper := pkgApp.NewMapper()
	statusWriter := pkgApp.NewStatusWriter(r.Client, r.Log.WithName("package-status-writer"))
	packageService := pkgApp.NewPackageService(mapper, backendSelector, statusWriter)

	// Registry ( Domain Registration, domain orchestrates mediator + service)
	pkgDomainInst := pkgDomain.New(r.PackageMediator, packageService, cache, eventsRecorder, r.Log.WithName("domain.package"))
	registry.RegisterDomain(packagev1alpha1.GroupVersion.WithKind("Package"), pkgDomainInst)

	// Controller registration
	return ctrl.NewControllerManagedBy(mgr).
		For(&packagev1alpha1.Package{}).
		Named("environments-package").
		WithEventFilter(predicate.Or(predicates.MeaningfulChangePredicate(), deletionRequested())).
		Complete(r)
}

// deletionRequested passes the update that sets an object's deletion
// timestamp. MeaningfulChangePredicate only passes spec changes, and marking
// an object for deletion does not change its spec — without this the
// reconciler would never run its delete path and the finalizer would keep
// the object forever.
func deletionRequested() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			return e.ObjectOld.GetDeletionTimestamp().IsZero() && !e.ObjectNew.GetDeletionTimestamp().IsZero()
		},
	}
}
