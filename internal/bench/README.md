# Benchmarks & Profiling — blanketops-environments-controller

Performance benchmarking and profiling framework for the BlanketOps Environments controller.

## Structure

```
bench/
├── README.md
├── BASELINES.md                      # Tracked baseline numbers per release
├── controller/
│   └── controller_bench_test.go      # Thin controller → mediator handoff
├── mediator/
│   ├── build_bench_test.go           # Build mediator chain
│   ├── deployment_bench_test.go      # Deployment mediator chain
│   ├── serviceunit_bench_test.go     # ServiceUnit mediator chain
│   └── chain_bench_test.go           # Generic mediator pattern overhead
├── domain/
│   └── domain_bench_test.go          # Domain logic (build, deploy, serviceunit)
├── observer/
│   └── observer_bench_test.go        # Observer notification cost
├── runtime/
│   └── runtime_bench_test.go         # Command router / GVK dispatch / worker pool
└── profile/
    └── pprof.go                      # pprof endpoint wiring for cmd/main.go
```

Maps to the controller's internal layout:

| bench package     | tests code in                          |
|-------------------|----------------------------------------|
| `controller/`     | `internal/controller/environments/`    |
| `mediator/`       | `internal/controller/mediators/`       |
| `domain/`         | `internal/domains/`                    |
| `observer/`       | `internal/controller/observers/`       |
| `runtime/`        | `internal/runtime/`                    |
| `profile/`        | wired into `cmd/main.go`              |

## Running Benchmarks

```bash
# All benchmarks
go test -bench=. -benchmem -count=5 ./bench/...

# Specific layer
go test -bench=. -benchmem -count=5 ./bench/mediator/
go test -bench=. -benchmem -count=5 ./bench/runtime/

# With CPU profile
go test -bench=BenchmarkBuildMediator -cpuprofile=cpu.prof ./bench/mediator/

# With memory profile
go test -bench=BenchmarkBuildMediator -memprofile=mem.prof ./bench/mediator/

# Compare before/after (install: go install golang.org/x/perf/cmd/benchstat@latest)
go test -bench=. -benchmem -count=10 ./bench/runtime/ > old.txt
# ... make changes ...
go test -bench=. -benchmem -count=10 ./bench/runtime/ > new.txt
benchstat old.txt new.txt
```

## Profiling the Controller

Wire pprof into `cmd/main.go` (see `bench/profile/pprof.go`):

```bash
# CPU profile (30s)
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30

# Heap
go tool pprof http://localhost:6060/debug/pprof/heap

# Goroutine dump
go tool pprof http://localhost:6060/debug/pprof/goroutine

# Mutex contention
go tool pprof http://localhost:6060/debug/pprof/mutex

# Quick runtime stats JSON
curl http://localhost:6060/debug/runtime

# Interactive web UI
go tool pprof -http=:8080 http://localhost:6060/debug/pprof/heap
```

## Baseline Policy

After every tagged release, run the full suite with `-count=10` and update `BASELINES.md`
with `benchstat` output. PRs that regress >10% on any hot path must justify the regression.
