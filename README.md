# BlanketOps Environments Controller

Kubernetes controller for BlanketOps Environments.

- This project wires Kubernetes CRDs to the `blanketops/environments` engine.
- The controller is intentionally thin — all business logic lives in the engine module.

## Architecture

The system is split into two parts:

### 🧠 Engine (`blanketops/environments`)

- Domain logic
- Resolution layer
- Mediators
- Infra adapters
- Secrets management
- Contracts

🎛 Controller (this project)

- Kubernetes reconcilers
- Watches CRDs
- Calls engine mediators
- Updates status

The controller does not contain business rules.
It orchestrates reconciliation.

## Supported Resources

The controller reconciles:

- Build
- Deployment
- Environment
- Package
- ServiceUnit
- GitRepository
- GitHubEvent
- Route (registered, but errors on every CR — no domain handler is registered for its GVK yet)
- Domain (registered, but Reconcile is a no-op stub)

CRD schemas for these Kinds live in the external `environments-api` module,
not in this repo. All behavior is delegated to the engine layer.

## Prerequisites

```bash
Go v1.26+
Docker
Kubernetes v1.25+
kubectl
```

## Development

Build tooling is [mage](https://magefile.org), not make — there is no Makefile in this repo.

Run locally:

```bash
mage run
```

Build and push image:

```bash
IMG=<registry>/environments-controller:<tag> mage dockerbuild dockerpush
```

Run the unit test suite:

```bash
mage test
```

This repo does not own cluster deployment manifests (no `config/manager`,
`config/default`, or Helm chart) — only `config/rbac/role.yaml` is generated
here and synced to the separate install repo that owns actual deployment.

## Release

Releases are tag-driven:

```bash
git tag v0.0.X
git push origin --tags
```

The pipeline will:

- Generate CHANGELOG.md
- Commit it to main
- Create GitHub Release
- Publish release notes

## Design Principles

- Thin controller
- Engine-driven architecture
- Explicit domain contracts
- Resolution-based orchestration

Infrastructure adapters isolated from Kubernetes layer

## License

Apache 2.0
