.PHONY: build test lint-rules clean run bench bench-all test-integration test-integration-docker docker-build replay-build replay replay-score

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

# Lint detection rules against pipeline ground truth (emitted event types,
# injected session fields, ATT&CK tags). Fast; no build needed.
lint-rules:
	$(GO) test ./internal/rulelint/...

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

# Score the rule corpus against the labelled ATBench trajectories.
#
# The full split is deliberate: ATBench rows are ordered by label, so a partial
# run returns a single-class prefix and reports a meaningless precision.
replay-score: replay-build
	./bin/agentshield-replay run \
	  --dataset AI45Research/ATBench \
	  --rules-dir rules/rules \
	  --output replay-score-atbench.json
	@echo "Report: replay-score-atbench.json"

# Build Docker engine image
docker-build:
	docker build -t agentshield-engine:test -f docker/engine.Dockerfile .
