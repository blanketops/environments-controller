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
		// ── Teardown: delete ClusterSecretStore and ExternalSecrets ─────────
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
				// Delete ClusterSecretStore (cluster-scoped, not owned by Environment)
				clusterStore := &unstructured.Unstructured{}
				clusterStore.SetAPIVersion("external-secrets.io/v1")
				clusterStore.SetKind("ClusterSecretStore")
				clusterStore.SetName(storeName)
				if err := d.client.Delete(ctx, clusterStore); err != nil && !errors.IsNotFound(err) {
					log.Error(err, "failed to delete ClusterSecretStore", "store", storeName)
				} else {
					log.Info("deleted ClusterSecretStore", "store", storeName)
				}

				// Delete namespaced SecretStore (owned by Environment, will be GC'd but explicit is better)
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
