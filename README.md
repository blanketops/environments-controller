# BlanketOps Environments Controller

Kubernetes controller for BlanketOps Environments.

- This project wires Kubernetes CRDs to the blanketops-environments engine.
- The controller is intentionally thin — all business logic lives in the engine module.

## Architecture

The system is split into two parts:

### 🧠 Engine (blanketops-environments)

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
- BuildTrigger
- Deployment
- Environment
- Package
- ServiceUnit
- GitHubEvent
- GitRepository

All behavior is delegated to the engine layer.

## Prerequisites

```bash
Go v1.24+
Docker
Kubernetes v1.25+
kubectl
```

## Development

Run locally:

```bash
make run
```

Build image:

```bash
make docker-build docker-push IMG=<registry>/blanketops-environments-controller:<tag>
```

Deploy to cluster:

```bash
make deploy IMG=<registry>/blanketops-environments-controller:<tag>
```

Uninstall:

```bash
make undeploy
make uninstall
```

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
