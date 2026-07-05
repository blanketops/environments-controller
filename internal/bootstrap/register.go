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

package bootstrap

import (
	kappctrlv1alpha1 "carvel.dev/kapp-controller/pkg/apis/kappctrl/v1alpha1"
	environmentsv1alpha1 "github.com/BlanketOps/environments-api/api/environments/v1alpha1"
	eventsv1alpha1 "github.com/BlanketOps/environments-api/api/events/v1alpha1"
	sourcesv1alpha1 "github.com/BlanketOps/environments-api/api/sources/v1alpha1"
	argoeventsv1alpha1 "github.com/argoproj/argo-events/pkg/apis/events/v1alpha1"
	kustomizev1 "github.com/fluxcd/kustomize-controller/api/v1"
	fluxcdsourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	buildapi "github.com/ntlaletsi70/blanketops-environments/pkg/apis/build/api"
	buildapp "github.com/ntlaletsi70/blanketops-environments/pkg/apis/build/application"
	gitrepoapi "github.com/ntlaletsi70/blanketops-environments/pkg/apis/gitrepository/api"
	shipwrightv1alpha1 "github.com/shipwright-io/build/pkg/apis/build/v1alpha1"
	shipwrightclientset "github.com/shipwright-io/build/pkg/client/clientset/versioned"
	pipelinev1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	// cacheEnvironment "github.com/ntlaletsi70/blanketops-environments-controller/internal/cache/environment"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/environments"
	eventsContr "github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/events"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/observers/build"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/observers/buildrun"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/observers/deployment"
	environment "github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/observers/environment"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/observers/githubevent"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/observers/gitrepository"
	"github.com/ntlaletsi70/blanketops-environments-controller/internal/controller/sources"
	runtimeinfra "github.com/ntlaletsi70/blanketops-environments-controller/internal/runtime"
)

func RegisterSchemes(scheme *runtime.Scheme) {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(environmentsv1alpha1.AddToScheme(scheme))
	// utilruntime.Must(externalsecretsv1.AddToScheme(scheme))
	// utilruntime.Must(externalsecretsv1beta1.AddToScheme(scheme))

	utilruntime.Must(eventsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(sourcesv1alpha1.AddToScheme(scheme))
	// utilruntime.Must(resultsv1.AddToScheme(scheme))
	utilruntime.Must(kappctrlv1alpha1.AddToScheme(scheme))
	utilruntime.Must(argoeventsv1alpha1.AddToScheme(scheme))

	utilruntime.Must(shipwrightv1alpha1.AddToScheme(scheme))
	// /utilruntime.Must(shipwrightv1beta1.AddToScheme(scheme))
	utilruntime.Must(pipelinev1beta1.AddToScheme(scheme))
	utilruntime.Must(gitrepoapi.AddToScheme(scheme))

	utilruntime.Must(fluxcdsourcev1.AddToScheme(scheme))
	utilruntime.Must(kustomizev1.AddToScheme(scheme))
}

// func EnsureServiceAccount(ctx context.Context, cfg *rest.Config) error {
// 	client, err := kubernetes.NewForConfig(cfg)
// 	if err != nil {
// 		return err
// 	}

// 	const (
// 		namespace = "default"              // adjust later
// 		name      = "environments-manager" // must match deployment
// 	)
// 	_, err = client.CoreV1().
// 		ServiceAccounts(namespace).
// 		Get(ctx, name, metav1.GetOptions{})

// 	if apierrors.IsNotFound(err) {
// 		_, err = client.CoreV1().
// 			ServiceAccounts(namespace).
// 			Create(ctx, &corev1.ServiceAccount{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:      name,
// 					Namespace: namespace,
// 				},
// 			}, metav1.CreateOptions{})
// 	}

// 	return err
// }

// func Apply(ctx context.Context, cfg *rest.Config, manifest []byte) error {
// 	dyn, err := dynamic.NewForConfig(cfg)
// 	if err != nil {
// 		return err
// 	}

// 	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)

// 	for {
// 		obj := &unstructured.Unstructured{}
// 		if err := decoder.Decode(obj); err != nil {
// 			// EOF is expected when stream ends
// 			if err.Error() == "EOF" {
// 				return nil
// 			}
// 			return err
// 		}

// 		// Skip empty documents (very important)
// 		if obj.Object == nil || obj.GetKind() == "" {
// 			continue
// 		}

// 		gvk := obj.GroupVersionKind()
// 		mapping := schema.GroupVersionResource{
// 			Group:    gvk.Group,
// 			Version:  gvk.Version,
// 			Resource: resourceName(gvk.Kind),
// 		}

// 		var ri dynamic.ResourceInterface
// 		if obj.GetNamespace() == "" {
// 			ri = dyn.Resource(mapping)
// 		} else {
// 			ri = dyn.Resource(mapping).Namespace(obj.GetNamespace())
// 		}

// 		_, err = ri.Apply(
// 			ctx,
// 			obj.GetName(),
// 			obj,
// 			metav1.ApplyOptions{
// 				FieldManager: "blanketops-bootstrap",
// 				Force:        true,
// 			},
// 		)

// 		if err != nil && !apierrors.IsAlreadyExists(err) {
// 			return fmt.Errorf("apply %s/%s failed: %w",
// 				obj.GetKind(), obj.GetName(), err)
// 		}
// 	}
// }

// resourceName converts Kind -> resource name for dynamic client.
// This is intentionally naive and sufficient for bootstrap manifests
// (CRDs, RBAC, core resources).
// func resourceName(kind string) string {
// 	return strings.ToLower(kind) + "s"
// }

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

	buildah := buildapi.NewBuildahProvider(mgr.GetClient(), mgr.GetScheme(), logger, recorder)
	kaniko := buildapi.NewKanikoProvider(mgr.GetClient(), mgr.GetScheme(), logger, recorder)
	buildpacks := buildapi.NewBuildpacksProvider(mgr.GetClient(), mgr.GetScheme(), logger, recorder)

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
