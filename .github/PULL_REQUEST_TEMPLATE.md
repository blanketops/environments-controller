
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
* [ ] Application / domain layer (no API surface change)
* [ ] CI / tooling / docs

## API impact

* [ ] No API surface change
* [ ] `v1alpha1` — free to change
* [ ] `v1beta1` — backwards-compatible only, deprecations allowed
* [ ] `v1` — **breaking change** (requires version bump + changelog entry + migration note)

<!-- If breaking: what breaks, and what must consumers do? -->

## Checklist

* [ ] `mage verify` passes locally
* [ ] `buf breaking` reviewed (failures justified above if pre-v1)
* [ ] Panic-free resolution — no `panic()` calls in resolution or domain layers
* [ ] Import paths use `gen/go/blanketops/...` for contract types
* [ ] BlanketOps labels present where required (`environments.blanketops.dev/*`)
* [ ] Conditions written via `core.SetCondition` at each domain pipeline stage
* [ ] Events emitted via `core.EventRecorder` for terminal outcomes
* [ ] ESP-0001 updated if contract semantics changed
* [ ] Commit messages follow Conventional Commits

## Notes for reviewer

<!-- Anything non-obvious: design trade-offs, deferred follow-ups, areas needing close attention -->
