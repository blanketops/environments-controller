package serviceunit

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	serviceunitResolution "github.com/ntlaletsi70/blanketops-environments/resolution/serviceunit"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Mediator struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

func New(c client.Client, scheme *runtime.Scheme, log logr.Logger) *Mediator {
	return &Mediator{
		Client: c,
		Scheme: scheme,
		Log:    log,
	}
}

func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *serviceunitResolution.ResolvedServiceUnit,
) error {

	log := m.Log.WithValues(
		"serviceunit", resolved.ServiceUnit.Name,
		"namespace", resolved.ServiceUnit.Namespace,
	)

	log.Info("mediator start")

	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedServiceUnit (resolver bug)")
	}

	spec := resolved.Spec

	log.Info("resolved serviceunit contract",
		"name", resolved.ServiceUnit.Name,
		"replicas", spec.Size,
		"image", spec.Image,
		"port", spec.ContainerPort,
	)

	return nil
}
