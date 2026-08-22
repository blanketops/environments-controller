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

// Package testsupport provides shared fixtures for the domain, mediator, and
// cache layer unit tests: a scheme matching production's RegisterSchemes
// (internal/bootstrap/register.go), a fake controller-runtime client builder
// with status-subresource support, a no-op event recorder, and helpers for
// encoding a Go map into the runtime.RawExtension contract format every
// BlanketOps CR spec/status uses.
package testsupport

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	kappctrlv1alpha1 "carvel.dev/kapp-controller/pkg/apis/kappctrl/v1alpha1"
	argoeventsv1alpha1 "github.com/argoproj/argo-events/pkg/apis/events/v1alpha1"
	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/blanketops/environments-api/api/events/v1alpha1"
	networksv1alpha1 "github.com/blanketops/environments-api/api/networks/v1alpha1"
	sourcesv1alpha1 "github.com/blanketops/environments-api/api/sources/v1alpha1"
	"github.com/blanketops/environments/core/events"
	gitrepoapi "github.com/blanketops/environments/pkg/apis/gitrepository/api"
	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	fluxcdsourcev1 "github.com/fluxcd/source-controller/api/v1"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	pipelinev1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	rawevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// externalSecretGVK is the GVK mediators create via unstructured.Unstructured
// (external-secrets.io has no typed scheme dependency in this repo). Fake
// clients need every GVK they'll Get/Create/List registered, even for
// unstructured objects, so it's added here alongside the typed schemes.
var externalSecretGVK = schema.GroupVersionKind{
	Group:   "external-secrets.io",
	Version: "v1",
	Kind:    "ExternalSecret",
}

var externalSecretListGVK = schema.GroupVersionKind{
	Group:   "external-secrets.io",
	Version: "v1",
	Kind:    "ExternalSecretList",
}

// providerConfigGVK is the GVK the gitrepository mediator creates via
// unstructured.Unstructured (pkg/providerconfig) — github.upbound.io has no
// typed scheme dependency in this repo either.
var providerConfigGVK = schema.GroupVersionKind{
	Group:   "github.upbound.io",
	Version: "v1beta1",
	Kind:    "ProviderConfig",
}

var providerConfigListGVK = schema.GroupVersionKind{
	Group:   "github.upbound.io",
	Version: "v1beta1",
	Kind:    "ProviderConfigList",
}

// NewScheme mirrors internal/bootstrap/register.go's RegisterSchemes, plus
// the ExternalSecret and ProviderConfig GVKs the build/deployment/
// githubevent/gitrepository/packages mediators create via
// unstructured.Unstructured.
func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(environmentsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(eventsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(sourcesv1alpha1.AddToScheme(scheme))
	utilruntime.Must(kappctrlv1alpha1.AddToScheme(scheme))
	utilruntime.Must(argoeventsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(shipwrightv1alpha1.AddToScheme(scheme))
	utilruntime.Must(pipelinev1beta1.AddToScheme(scheme))
	utilruntime.Must(gitrepoapi.AddToScheme(scheme))
	utilruntime.Must(fluxcdsourcev1.AddToScheme(scheme))
	utilruntime.Must(kustomizev1.AddToScheme(scheme))
	utilruntime.Must(networksv1alpha1.AddToScheme(scheme))

	scheme.AddKnownTypeWithName(externalSecretGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(externalSecretListGVK, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(providerConfigGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(providerConfigListGVK, &unstructured.UnstructuredList{})
	return scheme
}

// NewFakeClient builds a fake controller-runtime client seeded with objs,
// with status-subresource tracking enabled for every BlanketOps CR type —
// required for r.Status().Update()/Patch() to behave like a real API server
// (otherwise status writes silently apply to the main object and status
// assertions pass for the wrong reason).
func NewFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(NewScheme()).
		WithStatusSubresource(
			&environmentsv1alpha1.Build{},
			&environmentsv1alpha1.Deployment{},
			&environmentsv1alpha1.Environment{},
			&environmentsv1alpha1.Package{},
			&environmentsv1alpha1.ServiceUnit{},
			&eventsv1alpha1.GitHubEvent{},
			&sourcesv1alpha1.GitRepository{},
			&networksv1alpha1.Route{},
			&networksv1alpha1.Domain{},
		).
		WithObjects(objs...).
		Build()
}

// NoopRecorder returns an EventRecorder that safely discards every call —
// NewEventRecorder falls back to a no-op when given a type it doesn't
// recognize as either client-go recorder interface.
func NoopRecorder() *events.EventRecorder {
	return events.NewEventRecorder(nil)
}

// noopRawRecorder implements the raw k8s.io/client-go/tools/events.EventRecorder
// interface that mediator and provider constructors expect directly (as
// distinct from core/events.EventRecorder, the wrapper the domain layer
// uses) — discards every call.
type noopRawRecorder struct{}

func (noopRawRecorder) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...any) {
}

// NoopRawRecorder returns a raw client-go events.EventRecorder that discards
// every call, for constructing mediators and build providers in tests.
func NoopRawRecorder() rawevents.EventRecorder {
	return noopRawRecorder{}
}

// FakeExternalCache is an in-memory, JSON-serializing implementation of
// blanketops/environments' core/cache.ExternalCache, for testing this
// repo's internal/cache/* constructor wrappers. Values are marshaled on Set
// and unmarshaled on Get, matching how the real Redis/Memcached backends
// behave — a naive map[string]any passthrough would hide (de)serialization
// bugs. Mirrors the external library's own cache/internal/testutil fake,
// reimplemented here since that package is unexported outside its module.
type FakeExternalCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

// NewFakeExternalCache constructs an empty FakeExternalCache.
func NewFakeExternalCache() *FakeExternalCache {
	return &FakeExternalCache{data: make(map[string][]byte)}
}

func (f *FakeExternalCache) Set(_ context.Context, key string, val any, _ time.Duration) error {
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = b
	return nil
}

func (f *FakeExternalCache) Get(_ context.Context, key string, into any) (bool, error) {
	f.mu.Lock()
	b, ok := f.data[key]
	f.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(b, into); err != nil {
		return false, err
	}
	return true, nil
}

func (f *FakeExternalCache) Del(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *FakeExternalCache) DelPrefix(_ context.Context, prefix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			delete(f.data, k)
		}
	}
	return nil
}

// RawContract JSON-encodes m into a runtime.RawExtension, matching the
// spec.contract / status.contract field every BlanketOps CR uses instead of
// typed Kubernetes fields. Panics on marshal failure — the input is always a
// test-authored literal map, so a failure here is a test bug, not a
// runtime condition to handle gracefully.
func RawContract(m map[string]any) runtime.RawExtension {
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return runtime.RawExtension{Raw: raw}
}
