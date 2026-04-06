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

package githubevent

import (
	"context"

	eventsv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/events/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reconciler OBSERVES external systems and emits events.
// It MUST NOT mutate domain state.
type Reconciler struct {
	client.Client
	Recorder *core.EventRecorder
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
		r.Recorder.Normal(
			&ev,
			"ExternalEventObserved",
			"External system reported event activity",
		)
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = core.NewEventRecorder(mgr.GetEventRecorder("githubevent-observer"))

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
