package serviceunit

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	serviceunitv1alpha1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/core"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ServiceUnitDomain struct {
	cache  *core.Cache
	events *core.EventRecorder
	log    logr.Logger
}

func New(
	cache *core.Cache,
	events *core.EventRecorder,
	log logr.Logger) *ServiceUnitDomain {
	return &ServiceUnitDomain{
		cache:  cache,
		events: events,
		log:    log,
	}
}

func (d *ServiceUnitDomain) GVK() schema.GroupVersionKind {
	return serviceunitv1alpha1.GroupVersion.WithKind("ServiceUnit")
}

func (d *ServiceUnitDomain) Handle(ctx context.Context, cmd core.Command) error {
	su, ok := cmd.Obj.(*serviceunitv1alpha1.ServiceUnit)
	if !ok || su == nil {
		return fmt.Errorf("invalid object passed to ServiceUnitDomain: %T", cmd.Obj)
	}

	d.log.Info(
		"stub serviceunit domain invoked",
		"type", cmd.Type,
		"name", su.Name,
	)

	// No-op.
	// ServiceUnits are orchestrated by Deployment domain.
	return nil
}

func (d *ServiceUnitDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*serviceunitv1alpha1.ServiceUnit)
	return ok
}

func (d *ServiceUnitDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldSU, okOld := oldObj.(*serviceunitv1alpha1.ServiceUnit)
	newSU, okNew := newObj.(*serviceunitv1alpha1.ServiceUnit)
	if !okOld || !okNew {
		return false
	}

	return !reflect.DeepEqual(oldSU.Spec, newSU.Spec)
}

func (d *ServiceUnitDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*serviceunitv1alpha1.ServiceUnit)
	return ok
}
