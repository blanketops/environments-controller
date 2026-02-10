package gitrepository

import (
	"context"

	sourcesv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/sources/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments-mvp/pkg/gitrepository/application"
	"github.com/ntlaletsi70/blanketops-environments-mvp/pkg/gitrepository/domain"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder record.EventRecorder
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
		cond, ok := c.(map[string]interface{})
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
	r.Recorder = mgr.GetEventRecorderFor("sources-gitrepository")
	r.Status = application.NewStatusWriter()

	return ctrl.NewControllerManagedBy(mgr).
		For(&sourcesv1alpha1.GitRepository{}).
		Complete(r)
}
