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

// Package environments re-exports the environments-group reconciler types
// as public aliases. The reconcilers themselves stay in internal/controller,
// unchanged; this package exists only so that external test modules have a
// valid, non-internal import path to construct them against.
package environments

import (
	internalenv "github.com/blanketops/environments-controller/internal/controller/environments"
)

type (
	// EnvironmentReconciler reconciles an Environment object.
	EnvironmentReconciler = internalenv.EnvironmentReconciler
	// BuildReconciler reconciles a Build object.
	BuildReconciler = internalenv.BuildReconciler
	// DeploymentReconciler reconciles a Deployment object.
	DeploymentReconciler = internalenv.DeploymentReconciler
	// PackageReconciler reconciles a Package object.
	PackageReconciler = internalenv.PackageReconciler
	// ServiceUnitReconciler reconciles a ServiceUnit object.
	ServiceUnitReconciler = internalenv.ServiceUnitReconciler
)
