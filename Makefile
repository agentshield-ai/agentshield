.PHONY: build test clean run bench bench-all test-integration test-integration-docker docker-build

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

# Build Docker engine image
docker-build:
	docker build -t agentshield-engine:test -f docker/engine.Dockerfile .