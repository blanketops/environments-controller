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
This file owns DomainReconciler — the controller-runtime reconciler for the
Domain CR.

The reconciler is deliberately thin. It owns three responsibilities only:

 1. Fetch the Domain CR (NotFound → drop, the CR is gone).
 2. Resolve the raw contract into a ResolvedDomain (resolution layer).
 3. Hand the ResolvedDomain to DomainService, which maps, selects, dispatches,
    and writes status.

All business logic lives in pkg/apis/domain/application. The reconciler does
not build conditions, select providers, or touch the runtime resource
directly. A resolution failure is terminal for this generation — it is
logged and the request is dropped (no requeue) because re-running the same
bad contract will fail identically. Service errors are returned for
controller-runtime to requeue with backoff.

ACME is set by RegisterDomain (internal/bootstrap) before SetupWithManager
runs — it configures the cert-manager Issuer the Knative provider creates
for custom-strategy domains, and has no safe hardcoded default.
*/
package networks

import (
	"context"
	"time"

	networksv1alpha1 "github.com/blanketops/environments-api/api/networks/v1alpha1"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/predicates"
	domainapi "github.com/blanketops/environments/pkg/apis/domain/api"
	domainapp "github.com/blanketops/environments/pkg/apis/domain/application"
	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	domaindomain "github.com/blanketops/environments-controller/internal/domains/domain"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
)

// domainFinalizer gates deletion of a Domain CR until DomainService.Teardown
// has run successfully. See Reconcile for the add/check/remove lifecycle.
const domainFinalizer = "networks.blanketops.dev/domain-finalizer"

// DomainReconciler reconciles a Domain CR by resolving its contract and
// handing it to the domain application service.
type DomainReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	DomainService *domainapp.DomainService
	Runtime       *runtimeinfra.Runtime
	Recorder      events.EventRecorder

	// ACME configures the cert-manager Issuer the Knative provider creates
	// for custom-strategy domains. Must be set before SetupWithManager runs.
	ACME domainapi.ACMEConfig
}

// +kubebuilder:rbac:groups=networks.blanketops.dev,resources=domains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networks.blanketops.dev,resources=domains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networks.blanketops.dev,resources=domains/finalizers,verbs=update
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates;issuers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.internal.knative.dev,resources=clusterdomainclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *DomainReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "domain", "namespace", req.Namespace, "name", req.Name)
	ctx = logr.NewContext(ctx, log)

	log.Info("reconcile start")

	// Fetch Domain
	var domainCR networksv1alpha1.Domain
	if err := r.Get(ctx, req.NamespacedName, &domainCR); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("reconcile exit: domain not found (deleted)")
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to fetch domain")
		return ctrl.Result{}, err
	}
	log.Info("domain fetched", "generation", domainCR.Generation, "resourceVersion", domainCR.ResourceVersion)

	// Finalizer gate — determines cmd.Type
	cmdType := command.CmdUpdate
	if !domainCR.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&domainCR, domainFinalizer) {
			log.Info("reconcile exit: deletion in progress, finalizer already removed")
			return ctrl.Result{}, nil
		}
		cmdType = command.CmdDelete
	} else if !controllerutil.ContainsFinalizer(&domainCR, domainFinalizer) {
		controllerutil.AddFinalizer(&domainCR, domainFinalizer)
		if err := r.Update(ctx, &domainCR); err != nil {
			log.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer added")
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Construct core command
	cmd := command.Command{
		GVK:  networksv1alpha1.GroupVersion.WithKind("Domain"),
		Type: cmdType,
		Obj:  &domainCR,
	}

	log.Info("routing domain to core engine", "gvk", cmd.GVK.String(), "command", cmd.Type)

	// Execute domain logic via engine
	if err := r.Runtime.Engine.Execute(ctx, cmd); err != nil {
		log.Error(err, "engine execution failed")
		r.Recorder.Eventf(&domainCR, nil, corev1.EventTypeWarning, "EngineFailure", "Execute", "%v", err)
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
			var latest networksv1alpha1.Domain
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				return client.IgnoreNotFound(err)
			}
			controllerutil.RemoveFinalizer(&latest, domainFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
		log.Info("finalizer removed, deletion will proceed")
		return ctrl.Result{}, nil
	}
	// Persist status (retry-on-conflict) — create/update path only
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest networksv1alpha1.Domain
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}
		latest.Status = domainCR.Status
		return r.Status().Update(ctx, &latest)
	}); err != nil {
		log.Error(err, "failed to update domain status")
		return ctrl.Result{}, err
	}

	log.Info("domain status updated successfully")
	log.Info("reconcile done")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DomainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Logging & events
	r.Log = ctrl.Log.WithName("controllers").WithName("Domain")
	r.Recorder = mgr.GetEventRecorder("domain-controller")

	// Runtime Infrastructure
	cache := r.Runtime.Cache
	eventsRecorder := r.Runtime.Events
	registry := r.Runtime.Registry

	// Provider (runtime backend). Domain has no cross-cutting prerequisites
	// (no mediator) — DomainService dispatches to this directly.
	knativeBackend := domainapi.NewKnativeProvider(mgr.GetClient(), r.Log.WithName("backend.knative"), r.ACME)
	backendSelector := domainapp.NewBackendSelector(knativeBackend)

	// Service Layer (Mapper and StatusWriter, domain service for orchestration)
	mapper := domainapp.NewMapper()
	statusWriter := domainapp.NewStatusWriter(mgr.GetClient(), r.Log.WithName("domain-status-writer"))
	r.DomainService = domainapp.NewDomainService(mapper, statusWriter, backendSelector)

	// Registry (Domain Registration, domain orchestrates service + cache)
	domainDomain := domaindomain.New(r.DomainService, cache, eventsRecorder, r.Log.WithName("domain.domain"))
	registry.RegisterDomain(networksv1alpha1.GroupVersion.WithKind("Domain"), domainDomain)

	return ctrl.NewControllerManagedBy(mgr).
		For(&networksv1alpha1.Domain{}).
		Named("networks-domain").
		WithEventFilter(predicates.MeaningfulChangePredicate()).
		Complete(r)
}
