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

The controller orchestrates reconciliation; business rules live in the engine module.

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

## Design Principles

- Thin controller
- Engine-driven architecture
- Explicit domain contracts
- Resolution-based orchestration

Infrastructure adapters isolated from Kubernetes layer

## License

Apache 2.0
