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
package controller

import (
	"context"

	"github.com/go-logr/logr"
	networksv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/networks/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RouteReconciler reconciles a Route CR by resolving its contract and handing
// it to the route application service.
type RouteReconciler struct {
	Client client.Client
	Log    logr.Logger
}

// Reconcile fetches the Route, resolves its contract, and delegates to the
// application service. See file header for the responsibility split.
func (r *RouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// log := log.FromContext(ctx).WithValues("route", req.NamespacedName)

	var route networksv1alpha1.Route
	if err := r.Client.Get(ctx, req.NamespacedName, &route); err != nil {
		// NotFound: the CR was deleted. Owned runtime resources are garbage
		// collected via ownerReference — nothing to do here.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with the controller manager and
// declares the Route CR as the primary watched resource.
func (r *RouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networksv1alpha1.Route{}).
		Complete(r)
}
