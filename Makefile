.PHONY: build test clean run

# Go binary path
GO = /usr/local/go/bin/go

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

# Install dependencies
deps:
	$(GO) mod tidy
	$(GO) mod download