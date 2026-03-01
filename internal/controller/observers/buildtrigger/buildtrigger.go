package buildtrigger

import (
	"context"

	buildtriggerv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder record.EventRecorder
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("buildtrigger-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&buildtriggerv1.BuildTrigger{}).
		Complete(r)
}
