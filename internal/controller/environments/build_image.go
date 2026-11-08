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

// This file holds what the ServiceUnit and Deployment controllers share to
// follow a Build's image.
//
// A ServiceUnit of type BUILD names a Build and runs the image that Build
// last pushed. The buildrun observer records that image on the Build's
// status when a BuildRun succeeds. Nothing in the ServiceUnit or the
// Deployment changes when it does, so both controllers watch Builds and
// re-reconcile the objects that depend on the one whose image changed.
package environments

import (
	"context"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	serviceunitquery "github.com/blanketops/environments/pkg/apis/serviceunit/query"
	deploymentresolution "github.com/blanketops/environments/resolution/deployment/resolve"
	serviceunitresolution "github.com/blanketops/environments/resolution/serviceunit/resolve"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// buildImageChanged passes a Build update only when the image recorded on
// its status changed. Every other change to a Build, and its creation and
// deletion, mean nothing to the objects that run its image.
func buildImageChanged() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldBuild, ok := e.ObjectOld.(*environmentsv1alpha1.Build)
			if !ok {
				return false
			}
			newBuild, ok := e.ObjectNew.(*environmentsv1alpha1.Build)
			if !ok {
				return false
			}
			newImage, err := serviceunitquery.BuildImage(newBuild)
			if err != nil || newImage == "" {
				return false
			}
			// An old status that does not decode counts as no image.
			oldImage, _ := serviceunitquery.BuildImage(oldBuild)
			return oldImage != newImage
		},
	}
}

// serviceUnitsOfBuild lists the ServiceUnits of type BUILD that name the
// given Build. A ServiceUnit whose contract does not resolve is skipped: it
// has its own failure to report and cannot be said to depend on the Build.
func serviceUnitsOfBuild(ctx context.Context, c client.Reader, build types.NamespacedName) ([]types.NamespacedName, error) {
	var list environmentsv1alpha1.ServiceUnitList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	var out []types.NamespacedName
	for i := range list.Items {
		resolved, err := serviceunitresolution.ResolveServiceUnit(&list.Items[i])
		if err != nil {
			continue
		}
		if key, ok := serviceunitquery.BuildKey(resolved); ok && key == build {
			out = append(out, client.ObjectKeyFromObject(&list.Items[i]))
		}
	}
	return out, nil
}

// mapBuildToServiceUnits enqueues the ServiceUnits that run the Build's image.
func (r *ServiceUnitReconciler) mapBuildToServiceUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	units, err := serviceUnitsOfBuild(ctx, r.Client, client.ObjectKeyFromObject(obj))
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing serviceunits for build", "build", client.ObjectKeyFromObject(obj).String())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(units))
	for _, u := range units {
		reqs = append(reqs, reconcile.Request{NamespacedName: u})
	}
	return reqs
}

// mapBuildToDeployments enqueues the Deployments that list a ServiceUnit
// running the Build's image. A Deployment lists ServiceUnits by name, in its
// own namespace.
func (r *DeploymentReconciler) mapBuildToDeployments(ctx context.Context, obj client.Object) []reconcile.Request {
	log := ctrl.LoggerFrom(ctx).WithValues("build", client.ObjectKeyFromObject(obj).String())

	units, err := serviceUnitsOfBuild(ctx, r.Client, client.ObjectKeyFromObject(obj))
	if err != nil {
		log.Error(err, "listing serviceunits for build")
		return nil
	}
	if len(units) == 0 {
		return nil
	}
	wanted := make(map[types.NamespacedName]struct{}, len(units))
	for _, u := range units {
		wanted[u] = struct{}{}
	}

	var list environmentsv1alpha1.DeploymentList
	if err := r.List(ctx, &list); err != nil {
		log.Error(err, "listing deployments for build")
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		resolved, err := deploymentresolution.ResolveDeployment(&list.Items[i])
		if err != nil {
			continue
		}
		for _, name := range resolved.Spec.ServiceUnits {
			if _, ok := wanted[types.NamespacedName{Namespace: list.Items[i].Namespace, Name: name}]; ok {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
				break
			}
		}
	}
	return reqs
}
