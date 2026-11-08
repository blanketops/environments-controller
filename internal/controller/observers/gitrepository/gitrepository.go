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
Package gitrepository observes the Crossplane Repository object the
GitRepository domain's provider creates and reflects its Ready condition
back onto the owning GitRepository CR's status.

Reconcile lists Crossplane Repository objects labeled for the GitRepository
CR (rather than looking one up by a fixed name), reports StatePending when
none exist yet, and otherwise derives readiness from the first match's
status.conditions — mirroring the same "provider creates infrastructure,
a separate observer reports on its real-world state" split used by the
build and githubevent observers.
*/
package gitrepository

import (
	"context"

	sourcesv1alpha1 "github.com/blanketops/environments-api/api/sources/v1alpha1"
	"github.com/blanketops/environments/core/events"
	"github.com/blanketops/environments/pkg/apis/gitrepository/application"
	"github.com/blanketops/environments/pkg/apis/gitrepository/domain"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler observes the Crossplane Repository resource backing a
// GitRepository CR and reflects its Ready condition back onto the
// GitRepository's status.
type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *events.EventRecorder
}

var repositoryGVK = schema.GroupVersionKind{
	Group:   "repo.github.upbound.io",
	Version: "v1alpha1",
	Kind:    "Repository",
}

// Reconcile lists the Crossplane Repository objects labeled for this
// GitRepository, derives a domain.Result from their Ready condition (or
// StatePending if none exist yet), and writes it to status.
func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	// 1. Load the source GitRepository CR
	var repo sourcesv1alpha1.GitRepository
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. List Crossplane Repository objects by label
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(repositoryGVK)

	if err := r.List(
		ctx,
		&list,
		client.MatchingLabels{
			"sources.blanketops.dev/gitrepository": repo.Name,
		},
	); err != nil {
		return ctrl.Result{}, err
	}

	// 3. Derive domain result from observation
	var result domain.Result

	if len(list.Items) == 0 {
		result = domain.Result{
			State:  domain.StatePending,
			Reason: "repository not yet created",
		}
	} else {
		ready, reason := extractReady(list.Items[0])

		if ready {
			result = domain.Result{
				State:  domain.StateReady,
				Reason: "repository ready",
			}
		} else {
			result = domain.Result{
				State:  domain.StatePending,
				Reason: reason,
			}
		}
	}

	// 4. Write status (conditions-based)
	return ctrl.Result{}, r.Status.Write(ctx, &repo, result, nil)
}

func extractReady(obj unstructured.Unstructured) (bool, string) {
	conds, found, _ := unstructured.NestedSlice(
		obj.Object,
		"status",
		"conditions",
	)
	if !found {
		return false, "waiting for conditions"
	}

	for _, c := range conds {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}

		if cond["type"] == "Ready" {
			status, _ := cond["status"].(string)
			reason, _ := cond["reason"].(string)
			message, _ := cond["message"].(string)

			if status == string(corev1.ConditionTrue) {
				return true, "repository ready"
			}

			if message != "" {
				return false, message
			}

			return false, reason
		}
	}

	return false, "waiting for Ready condition"
}

// SetupWithManager registers the GitRepository observer with the
// controller manager, watching GitRepository CRs.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = events.NewEventRecorder(mgr.GetEventRecorder("sources-gitrepository"))
	r.Status = application.NewStatusWriter()

	return ctrl.NewControllerManagedBy(mgr).
		For(&sourcesv1alpha1.GitRepository{}).
		Complete(r)
}
