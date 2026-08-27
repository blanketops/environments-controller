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

// Package buildrun re-exports the buildrun-observer reconciler type as a
// public alias. The reconciler itself stays in
// internal/controller/observers/buildrun, unchanged; this package exists
// only so that external test modules have a valid, non-internal import path
// to construct it against.
package buildrun

import (
	internalbuildrun "github.com/blanketops/environments-controller/internal/controller/observers/buildrun"
)

type (
	// Reconciler observes Shipwright BuildRun objects and reflects their
	// terminal Succeeded condition back onto the owning Build CR's status.
	Reconciler = internalbuildrun.Reconciler
)
