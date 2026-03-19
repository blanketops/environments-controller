package githubevent

import (
	"context"

	eventsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/events/v1alpha1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler OBSERVES external systems and emits events.
// It MUST NOT mutate domain state.
type Reconciler struct {
	client.Client
	Recorder record.EventRecorder
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	// Observe Argo Sensor (or equivalent external object)
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Sensor",
	})

	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	labels := obj.GetLabels()
	eventName := labels["events.blanketops.dev/githubevent"]
	if eventName == "" {
		// Not related to a GitHubEvent
		return ctrl.Result{}, nil
	}

	namespace := obj.GetNamespace()
	if namespace == "" {
		namespace = "default"
	}

	var ev eventsv1alpha1.GitHubEvent
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      eventName,
	}, &ev); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ---- OBSERVE ONLY ----
	if r.Recorder != nil {
		r.Recorder.Event(
			&ev,
			"Normal",
			"ExternalEventObserved",
			"External system reported event activity",
		)
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorder("githubevent-observer")

	// IMPORTANT: watch the external resource, not GitHubEvent
	return ctrl.NewControllerManagedBy(mgr).
		For(&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "argoproj.io/v1alpha1",
				"kind":       "Sensor",
			},
		}).
		Complete(r)
}
