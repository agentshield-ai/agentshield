# AgentShield - Quick Start Guide

## Build and Start

```bash
cd /path/to/agentshield

# Build the engine
go build -o bin/agentshield ./cmd/agentshield/

# Generate an auth token
export AGENTSHIELD_AUTH_TOKEN=$(openssl rand -hex 32)

# Start in audit mode (log-only, no blocking)
./bin/agentshield serve --config config.yaml
```

## Verify

```bash
# Health check (no auth required)
curl http://localhost:8433/api/v1/health

# Evaluate a test event
curl -X POST http://localhost:8433/api/v1/evaluate \
  -H "Authorization: Bearer $AGENTSHIELD_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "event_id": "test-001",
    "tool": "exec",
    "fields": {
      "event_type": "tool_call",
      "command": "echo hello"
    }
  }'
```

## Check Alerts

```bash
# List alerts via API
curl -H "Authorization: Bearer $AGENTSHIELD_AUTH_TOKEN" \
  "http://localhost:8433/api/v1/alerts?limit=10"

# Or use the CLI
./bin/agentshield alerts
```

## List Loaded Rules

```bash
./bin/agentshield rules list
```

## Configuration

Edit `config.yaml` (see [configuration.md](configuration.md) for full reference):

```yaml
server:
  addr: "127.0.0.1"
  port: 8433

auth:
  token: "${AGENTSHIELD_AUTH_TOKEN}"

rules:
  dir: "./rules"

evaluation_mode: "audit"
log_level: "info"
```

## Enable LLM Triage (Optional)

For intelligent alert classification, configure a triage provider:

```yaml
triage:
  enabled: true
  provider: "openai"         # or "anthropic"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"
```

## Documentation

- [Configuration Reference](configuration.md)
- [API Documentation](api.md)
- [Rule Authoring](rules.md)
- [Triage System](triage.md)
- [Deployment Guide](deployment.md)
