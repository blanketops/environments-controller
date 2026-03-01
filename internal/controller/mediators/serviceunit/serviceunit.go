package serviceunit

import (
	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Mediator coordinates the various stages of the application lifecycle.
type Mediator struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// New creates a new Mediator with all the sub-reconcilers.
func New(c client.Client, scheme *runtime.Scheme, log logr.Logger) *Mediator {
	return &Mediator{
		Client: c,
		Scheme: scheme,
		Log:    log,
	}
}

// Reconcile handles the primary reconciliation loop for an Environment.
// func (mediator *Mediator) Reconcile(ctx context.Context, env *environmentv1.Environment) error {
// 	//create functions per sections as single responsibility actions

// 	return nil
// }
