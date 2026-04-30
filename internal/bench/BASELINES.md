# Benchmark Baselines — blanketops-environments-controller

Track performance baselines across releases. Update after every tagged release.

## How to Update

```bash
go test -bench=. -benchmem -count=10 ./bench/... > bench-$(git describe --tags).txt
```

Compare against previous:

```bash
benchstat bench-v0.1.0.txt bench-v0.2.0.txt
```

## Regression Policy

- >10% regression on any hot path benchmark: must justify in PR description
- >25% regression: blocked until resolved or explicitly approved
- Any new allocation in a zero-alloc path: blocked

---

## v0.1.0 — Initial Baselines

_Run benchmarks and paste `benchstat` output here._

```
TODO: go test -bench=. -benchmem -count=10 ./bench/... > bench-v0.1.0.txt
```
