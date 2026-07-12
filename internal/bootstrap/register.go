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

// register.go wires the controller manager's runtime at startup.
//
// It owns: registering every external and internal API type with the
// client-go scheme (RegisterSchemes), ensuring the manager's
// ServiceAccount exists (EnsureServiceAccount), applying raw manifests
// via the dynamic client (Apply), and registering the observer and CQRS
// reconcilers (RegisterObservers, RegisterControllers, RegisterBuild).
//
// It owns no reconciliation logic itself — that lives in internal/domains,
// internal/mediators, and internal/controller/*, plus the build domain's
// backend providers in the external blanketops-environments-core library.
// cmd/main.go is this file's only caller.
package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	kappctrlv1alpha1 "carvel.dev/kapp-controller/pkg/apis/kappctrl/v1alpha1"
	environmentsv1alpha1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/BlanketOps/environments-api/api/events/v1alpha1"
	sourcesv1alpha1 "github.com/BlanketOps/environments-api/api/sources/v1alpha1"
	buildapi "github.com/BlanketOps/environments/pkg/apis/build/api"
	buildapp "github.com/BlanketOps/environments/pkg/apis/build/application"
	gitrepoapi "github.com/BlanketOps/environments/pkg/apis/gitrepository/api"
	argoeventsv1alpha1 "github.com/argoproj/argo-events/pkg/apis/events/v1alpha1"
	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	fluxcdsourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	shipwrightclientset "github.com/shipwright-io/build/pkg/client/clientset/versioned"
	pipelinev1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/blanketops/environments-controller/internal/controller/environments"
	eventsContr "github.com/blanketops/environments-controller/internal/controller/events"
	"github.com/blanketops/environments-controller/internal/controller/observers/build"
	"github.com/blanketops/environments-controller/internal/controller/observers/buildrun"
	"github.com/blanketops/environments-controller/internal/controller/observers/deployment"
	environment "github.com/blanketops/environments-controller/internal/controller/observers/environment"
	"github.com/blanketops/environments-controller/internal/controller/observers/githubevent"
	"github.com/blanketops/environments-controller/internal/controller/observers/gitrepository"
	"github.com/blanketops/environments-controller/internal/controller/sources"
	runtimeinfra "github.com/blanketops/environments-controller/internal/runtime"
)

// RegisterSchemes adds every API group the controller and its dependent
// providers need to the runtime scheme: this repo's own environments,
// events, and sources types from environments-api, plus the external
// CRDs — Argo Events, Flux (source and kustomize controllers), Kapp
// Controller, Shipwright, and Tekton Pipelines — that the domains and
// mediators reconcile against.
func RegisterSchemes(scheme *runtime.Scheme) {
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
}

// EnsureServiceAccount creates the manager's ServiceAccount if it does not
// already exist. Safe to call on every startup.
func EnsureServiceAccount(ctx context.Context, cfg *rest.Config) error {
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	const (
		namespace = "default"              // adjust later
		name      = "environments-manager" // must match deployment
	)

	_, err = client.CoreV1().
		ServiceAccounts(namespace).
		Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().
			ServiceAccounts(namespace).
			Create(ctx, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
			}, metav1.CreateOptions{})
	}
	return err
}

// Apply server-side applies a multi-document YAML or JSON manifest using
// the dynamic client. Empty documents are skipped and AlreadyExists errors
// are treated as success, so callers can safely re-apply the same manifest.
func Apply(ctx context.Context, cfg *rest.Config, manifest []byte) error {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			// EOF is expected when the stream ends.
			if err.Error() == "EOF" {
				return nil
			}
			return err
		}

		// Skip empty documents (very important).
		if obj.Object == nil || obj.GetKind() == "" {
			continue
		}

		gvk := obj.GroupVersionKind()
		mapping := schema.GroupVersionResource{
			Group:    gvk.Group,
			Version:  gvk.Version,
			Resource: resourceName(gvk.Kind),
		}

		var ri dynamic.ResourceInterface
		if obj.GetNamespace() == "" {
			ri = dyn.Resource(mapping)
		} else {
			ri = dyn.Resource(mapping).Namespace(obj.GetNamespace())
		}

		_, err = ri.Apply(
			ctx,
			obj.GetName(),
			obj,
			metav1.ApplyOptions{
				FieldManager: "blanketops-bootstrap",
				Force:        true,
			},
		)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("apply %s/%s failed: %w",
				obj.GetKind(), obj.GetName(), err)
		}
	}
}

// resourceName converts Kind -> resource name for dynamic client.
// This is intentionally naive and sufficient for bootstrap manifests
// (CRDs, RBAC, core resources).
func resourceName(kind string) string {
	return strings.ToLower(kind) + "s"
}

// RegisterObservers wires up the observer reconcilers for Build, BuildRun,
// Deployment, Environment, GitHubEvent, and GitRepository resources.
func RegisterObservers(mgr ctrl.Manager) error {
	statusWriter := buildapp.NewStatusWriter(mgr.GetClient(), mgr.GetLogger().WithName("buildrun-status-writer"))

	if err := (&build.Reconciler{Client: mgr.GetClient(), Status: statusWriter}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&buildrun.Reconciler{Client: mgr.GetClient(), Status: statusWriter}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&deployment.Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&environment.Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&githubevent.Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&gitrepository.Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	return nil
}

// RegisterControllers wires up the primary CQRS reconcilers for the
// environments, events, and sources domains: GitRepository, GitHubEvent,
// Deployment, ServiceUnit, Package, and Environment. Route and Domain
// (networks) are registered separately once they're ready — see below.
func RegisterControllers(mgr ctrl.Manager, rt *runtimeinfra.Runtime) error {
	if err := (&sources.GitRepositoryReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Runtime: rt,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&eventsContr.GitHubEventReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Runtime: rt,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&environments.DeploymentReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Runtime: rt,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&environments.ServiceUnitReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Runtime: rt,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	// Route and Domain (networks) are intentionally not registered yet.
	// DomainMapping ownership migration, FQDN patch-back via SSA, and the
	// endpoint-vs-exposure charter split are deferred to v0.7.0.
	// if err := (&networks.RouteReconciler{
	// 	Client:  mgr.GetClient(),
	// 	Scheme:  mgr.GetScheme(),
	// 	Runtime: rt,
	// }).SetupWithManager(mgr); err != nil {
	// 	return err
	// }
	// if err := (&networks.DomainReconciler{
	// 	Client:  mgr.GetClient(),
	// 	Scheme:  mgr.GetScheme(),
	// 	Runtime: rt,
	// }).SetupWithManager(mgr); err != nil {
	// 	return err
	// }

	if err := (&environments.PackageReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Runtime: rt,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&environments.EnvironmentReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Runtime: rt,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	return nil
}

// RegisterBuild wires up the build domain's backend providers — Buildah,
// Kaniko, and Buildpacks, from blanketops-environments-core — behind a
// single BackendSelector and BuildService, then registers the resulting
// BuildReconciler. It's separate from RegisterControllers because it needs
// a Shipwright client, logger, and event recorder the manager doesn't
// otherwise construct.
func RegisterBuild(
	mgr ctrl.Manager,
	rt *runtimeinfra.Runtime,
	logger logr.Logger,
	recorder events.EventRecorder,
) error {
	shipClient, err := shipwrightclientset.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		return err
	}

	buildMapper := &buildapp.Mapper{}
	buildStatusWriter := &buildapp.StatusWriter{Client: mgr.GetClient()}

	// Backend providers — one per build strategy the platform supports.
	buildah := buildapi.NewBuildahProvider(mgr.GetClient(), mgr.GetScheme(), logger, recorder)
	kaniko := buildapi.NewKanikoProvider(mgr.GetClient(), mgr.GetScheme(), logger, recorder)
	buildpacks := buildapi.NewBuildpacksProvider(mgr.GetClient(), mgr.GetScheme(), logger, recorder)

	// CQRS build service: selector picks a backend, mapper translates the
	// CR into a backend-specific spec, statusWriter reports progress back.
	selector := buildapp.NewBackendSelector(buildah, kaniko, buildpacks)

	buildService := buildapp.NewBuildService(buildMapper, buildStatusWriter, selector)

	return (&environments.BuildReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Runtime:      rt,
		BuildClient:  shipClient,
		BuildService: buildService,
	}).SetupWithManager(mgr)
}
