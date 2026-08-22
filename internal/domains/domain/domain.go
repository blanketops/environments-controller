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
Package domain is reserved for the Domain CR's CQRS Domain implementation
(core/domain.Domain) — the piece that would let Domain CR reconciliation
route through the shared Engine the way Build, Deployment, GitHubEvent,
GitRepository, and Package already do.

Not yet implemented: this package is currently empty, and nothing imports
it. The Domain CR is instead reconciled directly by
internal/controller/networks.DomainReconciler, whose Reconcile is itself a
no-op stub (fetches the CR, returns immediately) — Domain CR support has
no real behavior anywhere in this controller yet.
*/
package domain
