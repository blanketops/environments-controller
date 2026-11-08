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

/*
Package packages observes kapp-controller App objects and reflects what they
report back onto the owning Package CR's status.

This exists because the Package domain can only request execution: it applies
the kapp App and returns, and the provider deliberately never reports a final
outcome. This observer is the other half. It watches the App directly (not
the Package), resolves the owner from the App's controlling owner reference,
and writes the outcome — applied, failed or still pending — through the
package StatusWriter, emitting an event when the outcome is terminal.
*/
package packages

import (
	"context"

	kappctrlv1alpha1 "carvel.dev/kapp-controller/pkg/apis/kappctrl/v1alpha1"
	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	"github.com/blanketops/environments/core/events"
	packageapi "github.com/blanketops/environments/pkg/apis/packages/api"
	"github.com/blanketops/environments/pkg/apis/packages/application"
	"github.com/blanketops/environments/pkg/apis/packages/domain"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler observes kapp App resources and feeds their state back to the
// owning Package CR's contract status and conditions.
type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *events.EventRecorder
}

// +kubebuilder:rbac:groups=kappctrl.k14s.io,resources=apps,verbs=get;list;watch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=packages,verbs=get;list;watch
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=packages/status,verbs=get;update;patch

// Reconcile resolves the Package that owns the App and writes the App's
// current state to its status. Apps not owned by a Package, and Packages that
// are gone or being deleted, are ignored.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "package-observer", "app", req.String())
	log.Info("reconcile start")

	var app kappctrlv1alpha1.App
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		log.Info("app not found, ignoring")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	packageName := owningPackage(&app)
	if packageName == "" {
		log.Info("skipping: app is not owned by a package")
		return ctrl.Result{}, nil
	}

	var pkg environmentsv1alpha1.Package
	if err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: packageName}, &pkg); err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.Info("skipping: owning package not found")
			return ctrl.Result{}, nil
		}
		log.Error(err, "failed to fetch owning package")
		return ctrl.Result{}, err
	}
	if !pkg.DeletionTimestamp.IsZero() {
		log.Info("skipping: owning package is being deleted")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("package", pkg.Name, "namespace", pkg.Namespace)

	result := packageapi.PackageResultFromApplicationState(packageapi.ApplicationStateFromApp(&app))
	log.Info("app observed", "phase", result.Phase, "success", result.Success)

	if r.Recorder != nil {
		switch result.Phase {
		case domain.PackagePhaseSucceeded:
			r.Recorder.Normal(&pkg, "PackageApplied", "kapp App %s applied the package", app.Name)
		case domain.PackagePhaseFailed:
			r.Recorder.Warn(&pkg, "PackageFailed", "kapp App %s failed: %s", app.Name, result.Message)
		}
	}

	return ctrl.Result{}, r.Status.Write(ctx, &pkg, result, nil)
}

// owningPackage returns the name of the Package that controls the App, or ""
// when the App is not controlled by one.
func owningPackage(app *kappctrlv1alpha1.App) string {
	owner := metav1.GetControllerOf(app)
	if owner == nil || owner.Kind != "Package" {
		return ""
	}
	if owner.APIVersion != environmentsv1alpha1.GroupVersion.String() {
		return ""
	}
	return owner.Name
}

// SetupWithManager registers the Package observer with the controller
// manager, watching kapp App resources rather than Package CRs — the App is
// what reports whether the package was actually applied.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = events.NewEventRecorder(mgr.GetEventRecorder("package-observer"))
	r.Status = application.NewStatusWriter(mgr.GetClient(), ctrl.Log.WithName("package-observer-status-writer"))

	return ctrl.NewControllerManagedBy(mgr).
		For(&kappctrlv1alpha1.App{}).
		Named("package-observer").
		Complete(r)
}
