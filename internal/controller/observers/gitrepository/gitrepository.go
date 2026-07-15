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

package gitrepository

import (
	"context"

	sourcesv1alpha1 "github.com/blanketops/environments-api/api/sources/v1alpha1"
	"github.com/blanketops/environments/core"
	"github.com/blanketops/environments/pkg/apis/gitrepository/application"
	"github.com/blanketops/environments/pkg/apis/gitrepository/domain"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder *core.EventRecorder
}

var repositoryGVK = schema.GroupVersionKind{
	Group:   "repo.github.upbound.io",
	Version: "v1alpha1",
	Kind:    "Repository",
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	// ------------------------------------------------
	// 1. Load the source GitRepository CR
	// ------------------------------------------------
	var repo sourcesv1alpha1.GitRepository
	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// 2. List Crossplane Repository objects by label
	// ------------------------------------------------
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

	// ------------------------------------------------
	// 3. Derive domain result from observation
	// ------------------------------------------------
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

	// ------------------------------------------------
	// 4. Write status (conditions-based)
	// ------------------------------------------------
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

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("sources-gitrepository"))
	r.Status = application.NewStatusWriter()

	return ctrl.NewControllerManagedBy(mgr).
		For(&sourcesv1alpha1.GitRepository{}).
		Complete(r)
}
