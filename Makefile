.PHONY: build test clean run bench bench-all bench-go bench-go-baseline bench-go-compare test-integration test-integration-docker docker-build replay-build replay

# Go binary path
GO ?= go

# Binary name
BINARY = agentshield

# Build the application
build:
	$(GO) build -o bin/$(BINARY) ./cmd/agentshield

# Run tests
test:
	$(GO) test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/
	$(GO) clean

# Run the application
run: build
	./bin/$(BINARY)

# Build benchmark runner
bench-build:
	$(GO) build -o bin/agentshieldbench ./cmd/agentshieldbench

# Run benchmark suite (usage: make bench SUITE=bench/suites/benign.yaml)
bench: bench-build
	./bin/agentshieldbench run --endpoint http://localhost:8433 --suite $(SUITE) --bench-root bench

# Run all benchmark suites
bench-all: bench-build
	./bin/agentshieldbench run-all --endpoint http://localhost:8433 --bench-root bench

# Microbench packages — keep this list in sync with .github/workflows/bench.yml
BENCH_PACKAGES = ./internal/engine/... ./internal/cache/... ./internal/evaluate/... ./internal/server/... ./pkg/sigma/...
BENCH_COUNT ?= 6
BENCH_TIME  ?= 1s

# Run Go microbenchmarks across the hot path with allocation tracking
bench-go:
	$(GO) test -run=^$$ -bench=. -benchmem -benchtime=$(BENCH_TIME) -count=$(BENCH_COUNT) $(BENCH_PACKAGES)

# Capture a baseline file for benchstat comparison (count=10 for stability)
bench-go-baseline:
	@mkdir -p bench/baseline
	$(GO) test -run=^$$ -bench=. -benchmem -benchtime=$(BENCH_TIME) -count=10 $(BENCH_PACKAGES) > bench/baseline/microbench.txt
	@echo "wrote bench/baseline/microbench.txt"

# Compare current code against the checked-in baseline
bench-go-compare:
	@test -f bench/baseline/microbench.txt || (echo "no baseline; run: make bench-go-baseline" && exit 1)
	@command -v benchstat >/dev/null 2>&1 || (echo "benchstat not installed; run: go install golang.org/x/perf/cmd/benchstat@latest" && exit 1)
	@mkdir -p bench/results
	$(GO) test -run=^$$ -bench=. -benchmem -benchtime=$(BENCH_TIME) -count=$(BENCH_COUNT) $(BENCH_PACKAGES) > bench/results/microbench.txt
	benchstat -delta-test=utest bench/baseline/microbench.txt bench/results/microbench.txt

# Install dependencies
deps:
	$(GO) mod tidy
	$(GO) mod download

# Integration tests (requires engine binary)
test-integration: build
	cd plugins/openclaw && npm run test:integration

# Integration tests via Docker
test-integration-docker: docker-build
	cd plugins/openclaw && AGENTSHIELD_ENGINE_MODE=docker npm run test:integration

# Build replay tool
replay-build:
	$(GO) build -o bin/agentshield-replay ./cmd/agentshield-replay

# Run replay (usage: make replay DATASET=nlile/misc-merged-claude-code-traces-v1)
replay: replay-build
	./bin/agentshield-replay run --dataset $(DATASET) --rules-dir rules/rules

# Build Docker engine image
docker-build:
	docker build -t agentshield-engine:test -f docker/engine.Dockerfile .
