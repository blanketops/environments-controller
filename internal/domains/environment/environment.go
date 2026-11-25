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

/*
Package environment implements the Environment resource domain.

The Environment domain is responsible for managing the lifecycle of
Environment resources. It receives commands from the Engine, resolves
resource specifications into validated contracts, and records reconciliation
outcomes through conditions and events.

Current stage:
  - Resolves the environment contract (secretStore.provider required)
  - Publishes resolved projection to cache
  - Sets standard conditions
  - On delete: tears down the ClusterSecretStore and ExternalSecrets
*/
package environment

import (
	"context"
	"fmt"
	"reflect"

	envv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	"github.com/blanketops/environments/cache/environment"
	"github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	"github.com/blanketops/environments/core/conditions"
	"github.com/blanketops/environments/core/events"
	environmentResolution "github.com/blanketops/environments/resolution/environment/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnvironmentDomain implements the Environment resource domain logic.
type EnvironmentDomain struct {
	// client is used for reads if needed during domain logic.
	client client.Client
	// scheme is the runtime scheme.
	scheme *runtime.Scheme
	// environmentCache provides generation-scoped, field-level caching for
	// Environment resources. Advisory only — misses fall through to full
	// computation; correctness never depends on a hit.
	environmentCache *environment.EnvironmentCache
	// events handles logging of Kubernetes events.
	events *events.EventRecorder
	// log is the logger instance for this domain.
	log logr.Logger
}

// New returns a new EnvironmentDomain configured with the necessary dependencies.
func New(
	c client.Client,
	scheme *runtime.Scheme,
	domainCache *cache.Cache,
	eventRecorder *events.EventRecorder,
	log logr.Logger,
) *EnvironmentDomain {
	return &EnvironmentDomain{
		client:           c,
		scheme:           scheme,
		environmentCache: environment.NewEnvironmentCache(domainCache),
		events:           eventRecorder,
		log:              log,
	}
}

// GVK tells the engine which CRD this domain handles.
func (d *EnvironmentDomain) GVK() schema.GroupVersionKind {
	return envv1alpha1.GroupVersion.WithKind("Environment")
}

// Handle executes command.Command operations routed by the Engine.
func (d *EnvironmentDomain) Handle(ctx context.Context, cmd command.Command) error {
	environmentCR, ok := cmd.Obj.(*envv1alpha1.Environment)
	if !ok || environmentCR == nil {
		return fmt.Errorf("invalid object passed to EnvironmentDomain: %T", cmd.Obj)
	}

	log := d.log.WithValues(
		"domain", "environment",
		"name", environmentCR.Name,
		"namespace", environmentCR.Namespace,
	)
	log.Info("handling environment command", "type", cmd.Type)

	nn := client.ObjectKeyFromObject(environmentCR)
	gen := environmentCR.GetGeneration()

	switch cmd.Type {
	case command.CmdCreate, command.CmdUpdate:
		// ── Stage 0: Resolve environment contract ─────────────────────────────
		log.Info("resolving environment contract")
		resolved, err := environmentResolution.ResolveEnvironment(environmentCR)
		if err != nil {
			log.Error(err, "environment resolution failed")
			d.events.FromError(environmentCR, "EnvironmentResolveFailed", err)
			conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentResolveFailed", conditions.ConditionFalse, "EnvironmentResolve", err.Error())
			return err
		}

		// ── Stage 1: Publish resolved contract to cache ───────────────────────
		if cerr := d.environmentCache.PublishResolved(ctx, nn, gen, resolved); cerr != nil {
			log.V(1).Info("resolved projection publish incomplete", "error", cerr.Error())
			d.events.FromError(environmentCR, "EnvironmentCacheFailed", cerr)
			conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentCacheFailed", conditions.ConditionFalse, "resolved projection publish incomplete", cerr.Error())
		}

		log.Info("environment resolved successfully")
		d.events.Normal(environmentCR, "EnvironmentResolve", "Environment specification resolved successfully")
		conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentResolved", conditions.ConditionTrue, "EnvironmentSpecResolved", "Environment specification resolved successfully")

		log.Info("environment cached successfully")
		d.events.Normal(environmentCR, "EnvironmentCache", "Environment specification cached successfully")
		conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentCached", conditions.ConditionTrue, "EnvironmentSpecCached", "Environment specification cached successfully")

		// ── Stage 2: Ready ────────────────────────────────────────────────────
		// Secret store connection validation deferred — next phase.
		// Observer ref-patching deferred — next phase.
		_ = resolved
		log.Info("environment ready")
		d.events.Normal(environmentCR, "EnvironmentReady", "Environment is ready")
		conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentReady", conditions.ConditionTrue, "EnvironmentReady", "Environment is ready")

		log.Info("environment domain handling complete")

	case command.CmdDelete:
		// ── Cascade: delete child CRs first (top-down ownership) ────────────
		// Children are linked to this Environment by the
		// environments.blanketops.dev/name label, NOT by ownerReference (the
		// GitHubEvent lives in a different namespace, which ownerReferences
		// cannot cross). Delete them and HOLD the Environment finalizer until
		// every child is gone, so each child's own finalizer runs its teardown
		// (GitHub repos, webhooks, Flux, kapp) while the Environment — which
		// those child teardowns resolve — still exists.
		remaining, cerr := d.cascadeDeleteChildren(ctx, environmentCR)
		if cerr != nil {
			log.Error(cerr, "cascade child deletion failed")
			d.events.FromError(environmentCR, "EnvironmentCascadeFailed", cerr)
			conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentDeleted", conditions.ConditionFalse, "EnvironmentCascadeFailed", cerr.Error())
			return cerr
		}
		if remaining > 0 {
			msg := fmt.Sprintf("waiting for %d child resource(s) to finish deleting", remaining)
			log.Info("cascade in progress; environment finalizer retained", "remaining", remaining)
			conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentDeleted", conditions.ConditionFalse, "EnvironmentCascadePending", msg)
			return fmt.Errorf("%s", msg)
		}

		// ── Teardown: delete ClusterSecretStore and ExternalSecrets ─────────
		// Only reached once every child is gone, so the secret store the
		// children needed during their own teardown is still present while
		// they run.
		log.Info("tearing down environment secret store and external secrets")

		// Resolve to get the provider and store name
		resolved, err := environmentResolution.ResolveEnvironment(environmentCR)
		if err != nil {
			log.Error(err, "environment resolution failed during teardown")
		} else if resolved != nil && resolved.Spec != nil && resolved.Spec.Contract != nil {
			contract := resolved.Spec.Contract
			provider := contract.SecretStore.Provider
			storeName := contract.SecretStore.StoreName

			// Default store names if not overridden
			if storeName == "" {
				switch provider {
				case "vault", "SECRET_STORE_PROVIDER_VAULT":
					storeName = "blanketops-environments-vault"
				case "aws", "SECRET_STORE_PROVIDER_AWS":
					storeName = "blanketops-environments-aws"
				case "azure", "SECRET_STORE_PROVIDER_AZURE":
					storeName = "blanketops-environments-azure"
				case "gcp", "SECRET_STORE_PROVIDER_GCP":
					storeName = "blanketops-environments-gcp"
				}
			}

			if storeName != "" {
				// NOTE: the ClusterSecretStore (blanketops-environments-aws
				// etc.) is cluster-shared, install-managed infrastructure — it
				// is referenced by every Environment AND by other consumers
				// (e.g. the Crossplane github-upjet provider credentials). The
				// Environment does NOT own it and MUST NOT delete it on
				// teardown: doing so breaks every other Environment and the
				// provider. Only resources this Environment actually created
				// are teared down below.

				// Delete namespaced SecretStore (owned by Environment)
				nsStore := &unstructured.Unstructured{}
				nsStore.SetAPIVersion("external-secrets.io/v1")
				nsStore.SetKind("SecretStore")
				nsStore.SetName(storeName)
				nsStore.SetNamespace(environmentCR.Namespace)
				if err := d.client.Delete(ctx, nsStore); err != nil && !errors.IsNotFound(err) {
					log.Error(err, "failed to delete SecretStore", "store", storeName)
				} else {
					log.Info("deleted SecretStore", "store", storeName)
				}

				// Delete ExternalSecrets in the environment namespace that belong to this environment
				esList := &unstructured.UnstructuredList{}
				esList.SetAPIVersion("external-secrets.io/v1")
				esList.SetKind("ExternalSecret")
				if err := d.client.List(ctx, esList, client.InNamespace(environmentCR.Namespace)); err == nil {
					for _, es := range esList.Items {
						if es.GetLabels()["blanketops.dev/domain"] == "environment" {
							if err := d.client.Delete(ctx, &es); err != nil && !errors.IsNotFound(err) {
								log.Error(err, "failed to delete ExternalSecret", "name", es.GetName())
							} else {
								log.Info("deleted ExternalSecret", "name", es.GetName())
							}
						}
					}
				}
			}
		}

		// ── Invalidate cache projection ─────────────────────────────────────
		if cerr := d.environmentCache.Invalidate(ctx, nn); cerr != nil {
			log.V(1).Info("projection invalidation failed", "error", cerr.Error())
		}
		log.Info("environment deletion marked")
		d.events.Normal(environmentCR, "EnvironmentDeletion", "environment deleted")
		conditions.SetCondition(&environmentCR.Status.Conditions, "EnvironmentDeleted", conditions.ConditionTrue, "EnvironmentMarkedDelete", "environment deleted")
		log.Info("environment domain handling complete")
	}

	return nil
}

// ── Predicate hooks ───────────────────────────────────────────────────────────

// CanCreate reports whether obj is an Environment.
func (d *EnvironmentDomain) CanCreate(obj client.Object) bool {
	_, ok := obj.(*envv1alpha1.Environment)
	return ok
}

// CanUpdate reports whether oldObj and newObj are both Environments whose
// specs differ.
func (d *EnvironmentDomain) CanUpdate(oldObj, newObj client.Object) bool {
	oldE, okOld := oldObj.(*envv1alpha1.Environment)
	newE, okNew := newObj.(*envv1alpha1.Environment)
	if !okOld || !okNew {
		return false
	}
	return !reflect.DeepEqual(oldE.Spec, newE.Spec)
}

// CanDelete reports whether obj is an Environment.
func (d *EnvironmentDomain) CanDelete(obj client.Object) bool {
	_, ok := obj.(*envv1alpha1.Environment)
	return ok
}

// childListGVKs are the composed CR kinds an Environment owns top-down.
// They are correlated to their Environment by the
// environments.blanketops.dev/name label (not ownerReferences — GitHubEvent
// lives in a different namespace than the Environment).
var childListGVKs = []schema.GroupVersionKind{
	{Group: "environments.blanketops.dev", Version: "v1alpha1", Kind: "BuildList"},
	{Group: "environments.blanketops.dev", Version: "v1alpha1", Kind: "ServiceUnitList"},
	{Group: "environments.blanketops.dev", Version: "v1alpha1", Kind: "DeploymentList"},
	{Group: "environments.blanketops.dev", Version: "v1alpha1", Kind: "PackageList"},
	{Group: "sources.blanketops.dev", Version: "v1alpha1", Kind: "GitRepositoryList"},
	{Group: "events.blanketops.dev", Version: "v1alpha1", Kind: "GitHubEventList"},
}

// cascadeDeleteChildren deletes every composed CR labelled for env across all
// namespaces and returns how many still exist. A non-zero count means teardown
// is still in progress — the children are running their own finalizers — and
// the caller must retain the Environment finalizer and retry. Kinds whose CRD
// is not installed are skipped. Delete is idempotent: children already marked
// for deletion are counted but not re-deleted.
func (d *EnvironmentDomain) cascadeDeleteChildren(ctx context.Context, env *envv1alpha1.Environment) (int, error) {
	remaining := 0
	for _, gvk := range childListGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := d.client.List(ctx, list, client.MatchingLabels{"environments.blanketops.dev/name": env.Name}); err != nil {
			if meta.IsNoMatchError(err) {
				continue // CRD not installed in this cluster
			}
			return 0, fmt.Errorf("list %s: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			item := &list.Items[i]
			remaining++
			if item.GetDeletionTimestamp().IsZero() {
				if err := d.client.Delete(ctx, item); err != nil && !errors.IsNotFound(err) {
					return 0, fmt.Errorf("delete %s %s/%s: %w", gvk.Kind, item.GetNamespace(), item.GetName(), err)
				}
			}
		}
	}
	return remaining, nil
}
