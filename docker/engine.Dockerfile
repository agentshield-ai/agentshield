# docker/engine.Dockerfile
# Multi-stage build for AgentShield engine.
# Used by integration tests in CI (AGENTSHIELD_ENGINE_MODE=docker).
FROM golang:1.24-bookworm AS builder

WORKDIR /src

# Cache dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/

# Build static binary (no CGO — uses modernc.org/sqlite)
RUN CGO_ENABLED=0 GOOS=linux go build -o /agentshield ./cmd/agentshield/

# Runtime stage — minimal image
FROM gcr.io/distroless/static-debian12

COPY --from=builder /agentshield /agentshield
COPY rules/ /rules/

EXPOSE 8433

ENTRYPOINT ["/agentshield"]
CMD ["serve", "--config", "/config.yaml"]
