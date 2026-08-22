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

package route

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	networksv1alpha1 "github.com/blanketops/environments-api/api/networks/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/events"
	routeapi "github.com/blanketops/environments/pkg/apis/route/api"
	routeapp "github.com/blanketops/environments/pkg/apis/route/application"
	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

const validRouteContract = `{"host":"api.dev.blanketops.online","runtime":"kubernetes-container","serviceUnitRef":{"name":"api"}}`

func newRouteCR(t *testing.T, raw string) *networksv1alpha1.Route {
	t.Helper()
	r := &networksv1alpha1.Route{
		ObjectMeta: metav1.ObjectMeta{Name: "my-route", Namespace: "default"},
	}
	if raw != "" {
		r.Spec.Contract = runtime.RawExtension{Raw: []byte(raw)}
	}
	return r
}

func newTestRouteDomain(t *testing.T, objs ...client.Object) (*RouteDomain, client.Client) {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()

	knative := routeapi.NewKnativeProvider(c, log)
	ingress := routeapi.NewIngressProvider(c, log)
	selector := routeapp.NewBackendSelector(knative, ingress)
	mapper := routeapp.NewMapper()
	statusWriter := routeapp.NewStatusWriter(c, log)
	svc := routeapp.NewRouteService(mapper, statusWriter, selector)

	domainCache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	recorder := events.NewEventRecorder(nil)

	return New(svc, domainCache, recorder, log), c
}

func TestRouteDomain_GVK(t *testing.T) {
	d := &RouteDomain{}
	gvk := d.GVK()
	if gvk.Kind != "Route" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "Route")
	}
}

func TestRouteDomain_CanCreate(t *testing.T) {
	d := &RouteDomain{}
	if !d.CanCreate(&networksv1alpha1.Route{}) {
		t.Error("CanCreate(*Route) = false, want true")
	}
	if d.CanCreate(&environmentsv1alpha1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestRouteDomain_CanDelete(t *testing.T) {
	d := &RouteDomain{}
	if !d.CanDelete(&networksv1alpha1.Route{}) {
		t.Error("CanDelete(*Route) = false, want true")
	}
	if d.CanDelete(&environmentsv1alpha1.Build{}) {
		t.Error("CanDelete(*Build) = true, want false")
	}
}

func TestRouteDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newRouteCR(t, `{"host":"a.example.com","runtime":"kubernetes-container","serviceUnitRef":{"name":"api"}}`),
			newObj: newRouteCR(t, `{"host":"b.example.com","runtime":"kubernetes-container","serviceUnitRef":{"name":"api"}}`),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newRouteCR(t, validRouteContract),
			newObj: newRouteCR(t, validRouteContract),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentsv1alpha1.Build{},
			newObj: newRouteCR(t, validRouteContract),
			want:   false,
		},
	}

	d := &RouteDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRouteDomain_Handle_InvalidObject(t *testing.T) {
	d := &RouteDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentsv1alpha1.Build{}}); err == nil {
		t.Fatal("Handle() with non-Route object = nil error, want error")
	}
}

func TestRouteDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	route := newRouteCR(t, "")
	d, _ := newTestRouteDomain(t, route)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: route})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
}

func TestRouteDomain_Handle_Create_Succeeds(t *testing.T) {
	route := newRouteCR(t, validRouteContract)
	d, c := newTestRouteDomain(t, route)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: route}); err != nil {
		t.Fatalf("Handle() create = %v, want nil", err)
	}

	var ingresses networkingv1.IngressList
	if err := c.List(context.Background(), &ingresses, client.InNamespace("default")); err != nil {
		t.Fatalf("list ingresses: %v", err)
	}
	if len(ingresses.Items) != 1 {
		t.Fatalf("expected exactly one Ingress to have been applied, got %d", len(ingresses.Items))
	}
}

func TestRouteDomain_Handle_Delete_RemovesWhatCreateApplied(t *testing.T) {
	route := newRouteCR(t, validRouteContract)
	d, c := newTestRouteDomain(t, route)
	ctx := context.Background()

	if err := d.Handle(ctx, command.Command{Type: command.CmdCreate, Obj: route}); err != nil {
		t.Fatalf("Handle() create (setup) = %v, want nil", err)
	}

	var before networkingv1.IngressList
	if err := c.List(ctx, &before, client.InNamespace("default")); err != nil {
		t.Fatalf("list ingresses (setup): %v", err)
	}
	if len(before.Items) != 1 {
		t.Fatalf("expected the setup create to have applied one Ingress, got %d", len(before.Items))
	}

	if err := d.Handle(ctx, command.Command{Type: command.CmdDelete, Obj: route}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}

	var after networkingv1.IngressList
	if err := c.List(ctx, &after, client.InNamespace("default")); err != nil {
		t.Fatalf("list ingresses: %v", err)
	}
	if len(after.Items) != 0 {
		t.Fatalf("expected Teardown to have deleted the Ingress, got %d remaining", len(after.Items))
	}
}
