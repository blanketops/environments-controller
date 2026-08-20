## What

<!-- One or two sentences. What does this PR change? -->

## Why

<!-- The intent. Link the issue if one exists: Closes #123 -->

## Domain

<!-- Mark all that apply -->

* [ ] `environments`
* [ ] `events`
* [ ] `sources`
* [ ] `networks`
* [ ] `common`
* [ ] Reconciler / controller-runtime wiring (no API surface change)
* [ ] CI / tooling / docs

## API impact

* [ ] No API surface change
* [ ] `v1alpha1` — free to change
* [ ] `v1beta1` — backwards-compatible only, deprecations allowed
* [ ] `v1` — **breaking change** (requires version bump + changelog entry + migration note)

<!-- If breaking: what breaks, and what must consumers do? -->

## Checklist

* [ ] `mage build` and `mage lint` pass locally
* [ ] `mage test` passes
* [ ] Business logic lives in the `environments` engine module, not in reconcilers here
* [ ] Import paths use `github.com/blanketops/environments-api/api/...` for CRD types
* [ ] BlanketOps labels present where required (`environments.blanketops.dev/*`)
* [ ] Conditions written via `core/conditions.SetCondition` at each domain pipeline stage
* [ ] Events emitted via the reconciler's `Recorder` for terminal outcomes
* [ ] Commit messages follow Conventional Commits

## Notes for reviewer

<!-- Anything non-obvious: design trade-offs, deferred follow-ups, areas needing close attention -->
