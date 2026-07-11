# blanketops-environments-controller - AI Agent Guide

## Architecture

This is a **thin Kubernetes controller**. It contains no business logic — it
watches CRDs and calls into the external engine module
(`github.com/ntlaletsi70/blanketops-environments`, mid-migration to
`github.com/BlanketOps/environments`) for domain logic, resolution, and
mediation. See [README.md](README.md) for the architecture split.

CRD type definitions (`*_types.go`, DeepCopy, etc.) do **not** live in this
repo — they're imported from the external `github.com/BlanketOps/environments-api`
module and registered onto the scheme in `internal/bootstrap/register.go`.
There is no `api/` directory here; that's intentional, not missing scaffolding.

## Project Structure

```
cmd/main.go                          Manager entry point
internal/bootstrap/register.go       Scheme registration + controller/observer wiring (main.go's only callee)
internal/runtime/                    Shared Runtime struct threaded through reconcilers
internal/controller/environments/    Primary reconcilers: Build, Deployment, Environment, Package, ServiceUnit
internal/controller/sources/         Primary reconciler: GitRepository
internal/controller/networks/        Reconcilers: Route, Domain (written but NOT registered — deferred to v0.7.0, see register.go)
internal/controller/events/          Primary reconciler: GitHubEvent (core k8s.io/api/events type)
internal/controller/observers/       Secondary/CQRS reconcilers watching engine-side state: build, buildrun, deployment, environment, githubevent, gitrepository
internal/domains/                    Domain orchestration layer, one package per resource (build, deployment, environment, githubevent, gitrepository, packages, serviceunit)
internal/mediators/                  Calls into the engine module per resource (build, deployment, githubevent, gitrepository, packages, serviceunit)
internal/cache/                      Read-side cache adapters per resource, backed by the engine's cache packages
internal/logging/                    Logger setup
pkg/controller/*, pkg/runtime/       Public aliases re-exporting internal/controller and internal/runtime for external test consumption
config/rbac/role.yaml                Generated RBAC (DO NOT EDIT — from `mage manifests`)
PROJECT                              Kubebuilder metadata (DO NOT EDIT) — source of truth for registered Kinds/groups
```

**Reconciled Kinds** (per `PROJECT`, domain `blanketops.dev` unless noted):
- `environments` group: Build, Deployment, Environment, Package, ServiceUnit
- `sources` group: GitRepository
- `networks` group: Route, Domain — **not currently registered** (see `internal/controller/networks/`)
- `events` group (domain `k8s.io`, core type): GitHubEvent

## Critical Rules

### Never Edit These (Auto-Generated)
- `config/rbac/role.yaml` - from `mage manifests`
- `PROJECT` - from `kubebuilder [OPTIONS]`

### Business Logic Lives in the Engine Module, Not Here
Domain rules, resolution, and mediation belong in the external engine module
(`.../blanketops-environments` → `BlanketOps/environments`). Controllers in
this repo call into `internal/domains` → `internal/mediators` → the engine;
they don't implement business rules directly.

### CRD Schemas Live in environments-api, Not Here
Don't add `api/<version>/*_types.go` to this repo for the reconciled Kinds —
those types come from `github.com/BlanketOps/environments-api` and are
registered in `internal/bootstrap/register.go` (`RegisterSchemes`).

### Engine Module Path Migration In Progress
Imports across this repo currently read
`github.com/ntlaletsi70/blanketops-environments/...`. The engine repo is
moving to `github.com/BlanketOps/environments`, but as of this writing the
upstream module's own `go.mod` still declares
`module github.com/ntlaletsi70/blanketops-environments` — so the import-path
rename here must wait until upstream retags under the new module path, or a
require on the new path will fail to resolve.

### Keep Project Structure
Do not move files around; `internal/bootstrap/register.go` and the CI
workflows assume the current layout.

## After Making Changes

Build tooling is [mage](https://magefile.org) (`magefile.go`), not make —
there is no Makefile in this repo.

**After editing RBAC markers in `internal/controller/*/*.go`:**
```
mage manifests  # Regenerate config/rbac/role.yaml from +kubebuilder:rbac markers
```

**After editing `*.go` files:**
```
mage lintfix    # Auto-fix code style (golangci-lint run --fix)
mage test       # Run unit tests
```

## Testing & Development

```bash
mage test              # Run unit tests (uses envtest: real K8s API + etcd)
mage run               # Run locally (uses current kubeconfig context)
mage build             # Build bin/manager
mage dockerbuild dockerpush   # IMG=<tag> env var selects the image ref
```

Other mage targets: `manifests`, `generate`, `fmt`, `vet`, `lint`, `lintconfig`, `clean`, `help`.

Tests use **Ginkgo + Gomega** (BDD style).

## Adding a New Reconciler

There's no scaffolding CLI wired up for this repo's actual pattern (CRD types
live externally). To add a reconciler for an existing `environments-api` Kind:

1. Add a domain package under `internal/domains/<resource>/` if one doesn't exist
2. Add a mediator under `internal/mediators/<resource>/` that calls into the engine module
3. Add the reconciler under `internal/controller/<group>/` (primary) or
   `internal/controller/observers/<resource>/` (CQRS observer)
4. Wire it into `internal/bootstrap/register.go` (`RegisterControllers` or
   `RegisterObservers`)
5. Add RBAC markers on the reconciler, then run `mage manifests`

**RBAC markers** in `internal/controller/*/*.go`:
```go
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=environments.blanketops.dev,resources=deployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
```

**Implementation rules:**
- **Idempotent reconciliation**: Safe to run multiple times
- **Re-fetch before updates**: `r.Get(ctx, req.NamespacedName, obj)` before `r.Update` to avoid conflicts
- **Structured logging**: `log := log.FromContext(ctx); log.Info("msg", "key", val)`
- **Owner references**: Enable automatic garbage collection (`SetControllerReference`)
- **Finalizers**: Clean up external resources (repos, deploy keys, etc.) — see `internal/mediators/deployment/manifests_repo.go` for an example

## Image Build Workflow

```bash
export IMG=<registry>/blanketops-environments-controller:tag
mage dockerbuild dockerpush

# Debug (once deployed via the separate install repo)
kubectl logs -n blanketops-environments-controller-system deployment/blanketops-environments-controller-controller-manager -c manager -f
```

This repo does not own cluster deployment manifests. Only
`config/rbac/role.yaml` is generated here (`mage manifests`) and synced to the
separate install repo, which owns the actual Kubernetes deploy/undeploy
workflow.

## References

- **Kubebuilder Book**: https://book.kubebuilder.io
- **controller-runtime FAQ**: https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md
- **Markers Reference**: https://book.kubebuilder.io/reference/markers.html
