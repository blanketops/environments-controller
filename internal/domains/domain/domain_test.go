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

package domain

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	networksv1alpha1 "github.com/blanketops/environments-api/api/networks/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/events"
	domainapi "github.com/blanketops/environments/pkg/apis/domain/api"
	domainapp "github.com/blanketops/environments/pkg/apis/domain/application"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	knnetworkingv1alpha1 "knative.dev/networking/pkg/apis/networking/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

// validDomainContract uses tlsStrategy: platform — the simplest path through
// KnativeProvider (ClusterDomainClaim only, no cert-manager Issuer/
// Certificate/ACME involved) — for the happy-path create/delete tests.
const validDomainContract = `{"host":"api.dev.blanketops.online","routeRef":{"name":"my-route"},"tlsStrategy":"platform"}`

func newDomainCR(t *testing.T, raw string) *networksv1alpha1.Domain {
	t.Helper()
	d := &networksv1alpha1.Domain{
		ObjectMeta: metav1.ObjectMeta{Name: "my-domain", Namespace: "default"},
	}
	if raw != "" {
		d.Spec.Contract = runtime.RawExtension{Raw: []byte(raw)}
	}
	return d
}

func newTestDomainDomain(t *testing.T, objs ...client.Object) (*DomainDomain, client.Client) {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()

	knative := domainapi.NewKnativeProvider(c, log, domainapi.ACMEConfig{})
	selector := domainapp.NewBackendSelector(knative)
	mapper := domainapp.NewMapper()
	statusWriter := domainapp.NewStatusWriter(c, log)
	svc := domainapp.NewDomainService(mapper, statusWriter, selector)

	domainCache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	recorder := events.NewEventRecorder(nil)

	return New(svc, domainCache, recorder, log), c
}

func TestDomainDomain_GVK(t *testing.T) {
	d := &DomainDomain{}
	gvk := d.GVK()
	if gvk.Kind != "Domain" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "Domain")
	}
}

func TestDomainDomain_CanCreate(t *testing.T) {
	d := &DomainDomain{}
	if !d.CanCreate(&networksv1alpha1.Domain{}) {
		t.Error("CanCreate(*Domain) = false, want true")
	}
	if d.CanCreate(&environmentsv1alpha1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestDomainDomain_CanDelete(t *testing.T) {
	d := &DomainDomain{}
	if !d.CanDelete(&networksv1alpha1.Domain{}) {
		t.Error("CanDelete(*Domain) = false, want true")
	}
	if d.CanDelete(&environmentsv1alpha1.Build{}) {
		t.Error("CanDelete(*Build) = true, want false")
	}
}

func TestDomainDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newDomainCR(t, `{"host":"a.example.com","routeRef":{"name":"my-route"},"tlsStrategy":"platform"}`),
			newObj: newDomainCR(t, `{"host":"b.example.com","routeRef":{"name":"my-route"},"tlsStrategy":"platform"}`),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newDomainCR(t, validDomainContract),
			newObj: newDomainCR(t, validDomainContract),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentsv1alpha1.Build{},
			newObj: newDomainCR(t, validDomainContract),
			want:   false,
		},
	}

	d := &DomainDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDomainDomain_Handle_InvalidObject(t *testing.T) {
	d := &DomainDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentsv1alpha1.Build{}}); err == nil {
		t.Fatal("Handle() with non-Domain object = nil error, want error")
	}
}

func TestDomainDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	domainCR := newDomainCR(t, "")
	d, _ := newTestDomainDomain(t, domainCR)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: domainCR})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
}

func TestDomainDomain_Handle_Create_Succeeds(t *testing.T) {
	domainCR := newDomainCR(t, validDomainContract)
	d, c := newTestDomainDomain(t, domainCR)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: domainCR}); err != nil {
		t.Fatalf("Handle() create = %v, want nil", err)
	}

	var claims knnetworkingv1alpha1.ClusterDomainClaimList
	if err := c.List(context.Background(), &claims); err != nil {
		t.Fatalf("list clusterdomainclaims: %v", err)
	}
	if len(claims.Items) != 1 {
		t.Fatalf("expected exactly one ClusterDomainClaim to have been applied, got %d", len(claims.Items))
	}
}

func TestDomainDomain_Handle_Delete_RemovesWhatCreateApplied(t *testing.T) {
	domainCR := newDomainCR(t, validDomainContract)
	d, c := newTestDomainDomain(t, domainCR)
	ctx := context.Background()

	if err := d.Handle(ctx, command.Command{Type: command.CmdCreate, Obj: domainCR}); err != nil {
		t.Fatalf("Handle() create (setup) = %v, want nil", err)
	}

	var before knnetworkingv1alpha1.ClusterDomainClaimList
	if err := c.List(ctx, &before); err != nil {
		t.Fatalf("list clusterdomainclaims (setup): %v", err)
	}
	if len(before.Items) != 1 {
		t.Fatalf("expected the setup create to have applied one ClusterDomainClaim, got %d", len(before.Items))
	}

	if err := d.Handle(ctx, command.Command{Type: command.CmdDelete, Obj: domainCR}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}

	var after knnetworkingv1alpha1.ClusterDomainClaimList
	if err := c.List(ctx, &after); err != nil {
		t.Fatalf("list clusterdomainclaims: %v", err)
	}
	if len(after.Items) != 0 {
		t.Fatalf("expected Teardown to have deleted the ClusterDomainClaim, got %d remaining", len(after.Items))
	}
}
