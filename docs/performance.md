# Performance

> **Last measured:** `5fa7315` on 2026-04-25 — first publication, captured
> locally at `count=3, benchtime=500ms`. The nightly CI job produces the
> authoritative `microbench-nightly` artifact; this page is refreshed on
> releases or via `make bench-go-baseline`.

This page reports measured latency, allocations, throughput, and concurrency
behaviour for the AgentShield evaluation pipeline. All numbers come from the
Go microbenchmark harness in `internal/{engine,cache,evaluate,server}` and
`pkg/sigma`, gated in CI so PRs exceeding the regression thresholds below
fail.

## Methodology

| Aspect | Value |
|---|---|
| Tool | `go test -bench=. -benchmem` |
| Comparison | `benchstat` (golang.org/x/perf), Mann-Whitney U-test, α = 0.05 |
| Regression gate | +10% on sec/op or allocs/op vs base branch, per-bench |
| PR run | `count=6`, `benchtime=500ms` (gated) |
| Nightly run | `count=20`, `benchtime=2s` (un-gated; produces baseline) |

## Hardware

| Aspect | Value |
|---|---|
| Reporting machine | 4× Intel Xeon @ 2.10 GHz (linux/amd64) |
| Go version | 1.24 |
| CI runner | GitHub Actions `ubuntu-latest` (4 vCPU at time of writing) |
| CI variance | ±15% is normal for shared `ubuntu-latest` runners; this is why the gate uses a 10% threshold + statistical significance |

For headline numbers (e.g. blog posts, sales decks) re-run on dedicated hardware via `make bench-go-baseline`. The regression gate is not affected — it always compares same-runner-to-same-runner.

## Hot-path latency

Single-request, no concurrency, cache enabled.

| Stage | ns/op | allocs/op | B/op | Notes |
|---|---:|---:|---:|---|
| `pkg/sigma` `Detection.Matches` (single rule, match) | 491 | 0 | 0 | Pure detection; zero-alloc steady state |
| `pkg/sigma` `Detection.Matches` (single rule, no-match) | 47 | 0 | 0 | Short-circuit on first failed atom |
| `pkg/sigma` `Detection.Matches` (complex rule) | 825 | 0 | 0 | File-access pattern with multiple atoms |
| `internal/engine.Engine.Evaluate` (1 rule, match) | 700 | 4 | 443 | Engine adds metadata wrapping over `pkg/sigma` |
| `internal/engine.Engine.Evaluate` (1 rule, no-match) | 398 | 1 | 24 | |
| `internal/evaluate.Evaluator.EvaluateWithContext` (mock engine, cache miss) | 3,500 | 27 | 1,720 | Includes field merging, cache-key compute, cache write |
| `internal/evaluate.Evaluator.EvaluateWithContext` (mock engine, cache hit) | 1,840 | 16 | 672 | Cache short-circuit before rule eval |
| `internal/server.handleEvaluate` (in-process, cache miss) | 113,000 | 105 | 12,316 | Includes chi routing, JSON decode/encode, store insert |
| `internal/server.handleEvaluate` (full TCP via `httptest.NewServer`) | 332,000 | 166 | 12,544 | Loopback + HTTP/1.1 framing |

The bulk of HTTP-handler cost is JSON serialisation and the SQLite alert
write, not rule evaluation. P2-C (binary transport) and P2-D (in-process Go
API) target this overhead.

## Cache hit / miss

| Operation | ns/op | allocs/op |
|---|---:|---:|
| `VerdictCache.Get` (hit, serial) | 98 | 0 |
| `VerdictCache.Get` (miss, serial) | 43 | 0 |
| `VerdictCache.Set` (warm key, serial) | 47 | 0 |
| `VerdictCache.Get` (parallel, GOMAXPROCS=4) | 145 | 0 |
| `VerdictCache` mixed 90/10 (parallel) | 142 | 0 |

`Get` currently takes an exclusive `Lock()` because LRU `MoveToFront` mutates
state (`internal/cache/cache.go:82`). Parallel benches show ~50% slowdown vs
serial at GOMAXPROCS=4; the contention cost rises with core count. Issue
#28 (P2-A) tracks splitting the read path; the `BenchmarkVerdictCache_GetParallel` bench is the baseline that fix must beat.

## Cache-key compute

`CacheKeyWithFields` is on the hot path of every cache miss. SHA-256 over a
sorted concatenation of args + fields.

| Field count | ns/op | allocs/op | B/op |
|---:|---:|---:|---:|
| 4 | 1,580 | 17 | 544 |
| 16 | 4,400 | 41 | 1,120 |
| 64 | 17,500 | 137 | 3,552 |

The cost grows roughly linearly with field count. P2-E (lazy session field
computation) reduces field count on the request when no rule references
session fields.

## Allocations

| Path | allocs/op | B/op |
|---|---:|---:|
| Engine evaluate (1 rule, match) | 4 | 443 |
| Evaluator (cache miss, mock engine) | 27 | 1,720 |
| Evaluator (cache hit) | 16 | 672 |
| Full handler (in-process) | 105 | 12,316 |
| Full handler (HTTP) | 166 | 12,544 |

The handler-level alloc count is dominated by JSON encode/decode and the
SQLite store write. P2-B (sigmalite zero-alloc field reuse) and P2-C
(binary transport) are the levers.

## Concurrency

| Bench | Serial ns/op | Parallel ns/op (4 cores) | Effective speedup |
|---|---:|---:|---:|
| `pkg/sigma.Detection.Matches` (complex) | 825 | 325 | 2.5× |
| `internal/engine.Engine.Evaluate` (100 rules) | 46,500 | 13,800 | 3.4× |
| `internal/cache.VerdictCache.Get` | 98 | 145 | 0.7× (slowdown) |
| `internal/evaluate` (cache miss) | 3,500 | 3,400 | ~1× |
| `internal/server.handleEvaluate` (HTTP) | 332,000 | 194,000 | 1.7× |

The cache parallel slowdown is the structural bottleneck at high core counts;
sigma and engine scale near-linearly with cores.

## Rule scaling

`Engine.Evaluate` over varying rule counts. Validates the
`docs/architecture.md` "sub-millisecond per event" claim across realistic rule
sets.

| Loaded rules | ns/op | µs/op | Notes |
|---:|---:|---:|---|
| 1 | 700 | 0.7 | One match-rule (steady state) |
| 10 | 5,000 | 5.0 | First match short-circuits the rest |
| 100 | 46,500 | 47 | Linear in rule count |
| 1,000 | 480,000 | 480 | Approaching the 1ms boundary |

**Conclusion:** the "sub-millisecond" claim holds at today's rule corpus
(~60 rules in `rules/rules/ai_agent/`) and at 1,000 synthetic rules
(~480 µs). Linear scaling implies the claim breaks somewhere around
~2,000 rules, which is well above current and projected production rule
sets. P2-B (zero-alloc reuse) and atom-tree pre-indexing would push this
further if rule corpus growth ever demands it.

## Triage path

The fast triage stage adds a synchronous LLM call, so latency is dominated
by the provider. AgentShield's harness reports wall-clock p50/p95 for
triage-enabled paths through `cmd/agentshieldbench`; representative numbers
from the existing bench suite (Anthropic Claude Haiku 4.5, prod):

| Path | Latency p50 | Latency p95 |
|---|---:|---:|
| Rule-only (no triage) | <1 ms | <5 ms |
| Triage-enabled, alert path | ~3 s | ~5 s |

These cross-reference and validate the "approximately 4 seconds" claim at
[docs/architecture.md](architecture.md). They depend on the triage provider;
for offline/air-gapped deployments triage is configured off entirely.

## Throughput

`BenchmarkHandleEvaluate_Concurrent` measures throughput of the full HTTP
handler under `b.RunParallel` on a 4-vCPU box: ~194 µs/op gives an upper
bound of about **20,000 req/s** on this hardware with cache miss on every
request. With realistic cache hit ratios (cache-warm benchmarks coming in
issue #36 / P4-B against HuggingFace traces), effective throughput is
substantially higher.

This is independent of the per-IP rate limit documented in
[docs/api.md](api.md) (default ~100 req/min/IP, burst 10) — the rate limit
applies at the middleware layer and protects the server from a single
abusive client; the throughput number above is an aggregate ceiling across
all clients.

## Reproducibility

```bash
# Run all microbenches locally (count=6, benchtime=1s — about 4 minutes)
make bench-go

# Capture a baseline file (count=10 — about 8 minutes)
make bench-go-baseline

# Compare current code against the checked-in baseline
make bench-go-compare

# Run a single bench
go test -run='^$' -bench=BenchmarkVerdictCache_GetParallel \
    -benchmem -count=10 ./internal/cache/...
```

`benchstat` is required for `bench-go-compare`:

```bash
go install golang.org/x/perf/cmd/benchstat@latest
```

## CI gate

Every PR runs the `microbench` job in `.github/workflows/bench.yml`. Behaviour:

1. Bench PR HEAD with `count=6, benchtime=500ms`.
2. Bench `origin/<base_ref>` HEAD with the same settings via `git worktree`.
3. Compare with `benchstat -alpha 0.05`.
4. `scripts/bench_gate.sh` exits non-zero on any row in the `sec/op` or
   `allocs/op` table that shows ≥10% regression with p<0.05.
5. The benchstat diff is posted as a sticky PR comment and uploaded as the
   `microbench-diff` artifact.

To override the threshold for a specific run (e.g. expected regression from
a measurable trade-off), set `REGRESSION_THRESHOLD_PCT` in the workflow env
or comment in the PR description and request explicit reviewer ack.

## Related docs

- [Architecture](architecture.md) — pipeline overview
- [Configuration](configuration.md) — cache size, TTL, mode tuning
- [API](api.md) — request/response shape and rate limits
