---
name: agentshield
description: "AI Agent Detection & Response — real-time security monitoring with Sigma rules and LLM-powered triage"
homepage: "https://github.com/agentshield-ai/agentshield"
user-invocable: true
metadata: {"openclaw":{"requires":{"anyBins":["go","curl"]},"os":["darwin","linux"]}}
---

# AgentShield

**AI Agent Detection & Response (AADR)** — real-time security monitoring with Sigma rules and LLM-powered triage.

## What is AgentShield?

AgentShield is a Go security engine that monitors AI agent tool calls in real-time. It evaluates each call against Sigma security rules and optionally routes suspicious events through LLM triage for context-aware verdicts. It runs as a single binary with no external runtime dependencies.

## Architecture

Single Go binary (`agentshield`) containing:
- HTTP server (Chi router, `127.0.0.1:8433` by default)
- Sigma rule engine (forked sigmalite in `pkg/sigma/`)
- SQLite alert/feedback store
- LLM triage (OpenAI or Anthropic provider, optional)
- Bearer token authentication (constant-time comparison)
- Per-IP rate limiting (~100 req/min, burst 10)

One systemd user service: `agentshield-engine.service` (Linux) or one launchd agent: `ai.agentshield.engine` (macOS).

## Installation

### Quick Install

```bash
./install.sh
```

The installer:
1. Detects platform (linux/darwin) and architecture (amd64/arm64)
2. Downloads binary from GitHub releases (falls back to `go install`)
3. Creates `~/.agentshield/` directory (rules, config, database)
4. Clones sigma-ai rules from `agentshield-ai/sigma-ai`
5. Generates a 64-character auth token
6. Writes `~/.agentshield/config.yaml`
7. Creates a systemd user service (Linux) or launchd agent (macOS)
8. Patches OpenClaw plugin configuration via `openclaw config patch`
9. Starts the service and runs a health check

### Manual Installation

```bash
# Build from source
go build ./cmd/agentshield/

# Create directory structure
mkdir -p ~/.agentshield/rules

# Clone rules
git clone --depth 1 https://github.com/agentshield-ai/sigma-ai.git ~/.agentshield/rules

# Generate auth token
openssl rand -hex 32

# Create config.yaml (see Configuration section)
# Start the engine
~/.agentshield/agentshield-engine serve --config ~/.agentshield/config.yaml
```

## Configuration

Config file: `~/.agentshield/config.yaml`

```yaml
server:
  addr: "127.0.0.1"
  port: 8433

auth:
  token: "your-64-char-token-here"

rules:
  dir: "~/.agentshield/rules"
  hot_reload: true

store:
  sqlite_path: "~/.agentshield/agentshield.db"
  retention_days: 90
  cleanup_interval_hours: 24

evaluation_mode: "enforce"   # enforce | audit | shadow

log_level: "info"

# triage:
#   enabled: true
#   provider: "openai"        # openai | anthropic
#   model: "gpt-4o-mini"
#   api_key: "sk-..."
#   max_tokens: 500
#   timeout_sec: 10
#   health_check_mode: "full"  # full | connectivity
```

### Environment Variable Overrides

| Variable | Overrides |
|----------|-----------|
| `AGENTSHIELD_PORT` | `server.port` |
| `AGENTSHIELD_ADDR` | `server.addr` |
| `AGENTSHIELD_AUTH_TOKEN` | `auth.token` |
| `AGENTSHIELD_RULES_DIR` | `rules.dir` |
| `AGENTSHIELD_DB_PATH` | `store.sqlite_path` |
| `AGENTSHIELD_MODE` | `evaluation_mode` |
| `AGENTSHIELD_LOG_LEVEL` | `log_level` |
| `AGENTSHIELD_TRIAGE_API_KEY` | `triage.api_key` |

### Authentication

A 32+ character token is mandatory. Without it, the server refuses to start. The installer generates a 64-character hex token automatically.

```bash
# Generate manually
openssl rand -hex 32

# Or via Python
python3 -c "import secrets; print(secrets.token_hex(32))"
```

### Evaluation Modes

- **enforce**: Blocks tool calls matching rules. Use in production.
- **audit**: Logs alerts without blocking. Default for testing.
- **shadow**: Silent monitoring. No user-visible alerts.

## CLI Commands

```bash
# Start the server
agentshield serve --config ~/.agentshield/config.yaml [--daemon] [--verbose]

# Check server status (queries /api/v1/health)
agentshield status [--verbose]

# List recent alerts
agentshield alerts [-l LIMIT] [-s SEVERITY] [--since RFC3339] [-r RULE]

# List loaded rules
agentshield rules list

# Reload rules (sends SIGHUP to running server)
agentshield rules reload

# Analyze rule performance using feedback data
agentshield refine [rule-name] [--apply] [--threshold FP_RATE]

# Show version
agentshield version
```

### Service Management

**Linux (systemd user service):**
```bash
systemctl --user status agentshield-engine
systemctl --user start agentshield-engine
systemctl --user stop agentshield-engine
systemctl --user restart agentshield-engine
journalctl --user -u agentshield-engine -f
```

**macOS (launchd):**
```bash
launchctl load ~/Library/LaunchAgents/ai.agentshield.engine.plist
launchctl unload ~/Library/LaunchAgents/ai.agentshield.engine.plist
tail -f ~/.agentshield/engine.log
```

## API Endpoints

All endpoints are under `/api/v1/`. Authentication via `Authorization: Bearer <token>` header.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/evaluate` | POST | Evaluate a tool call against rules + triage |
| `/api/v1/health` | GET | Health check (minimal info, works unauthenticated) |
| `/api/v1/alerts` | GET | Query stored alerts (paginated, filterable) |
| `/api/v1/feedback` | POST | Submit alert feedback (false_positive, true_positive, improvement) |
| `/api/v1/feedback?rule=<name>` | GET | Query feedback + FP rate for a rule |

### Server Limits

- Max request body: 1 MB
- Max field value: 10 KB
- Max fields per request: 100
- Rate limit: ~100 req/min per IP, burst 10
- Request timeout: 30 seconds

## LLM Triage

Optional. When enabled, the engine sends suspicious events to an LLM for context-aware analysis.

Supported providers: `openai`, `anthropic`.

```yaml
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "sk-..."
  max_tokens: 500
  timeout_sec: 10
  health_check_mode: "full"   # "full" runs a model call; "connectivity" just checks /v1/models
```

Triage returns a verdict (`block`, `allow`, `investigate`), confidence (0-1), and reasoning. In the OpenClaw plugin, a high-confidence allow (>0.8) from triage overrides rule-based alerts.

## Sigma Rules

Rules are stored in `~/.agentshield/rules/`. The engine loads all `.yml`/`.yaml` files from this directory.

### Rule Format

Standard Sigma format adapted for agent tool monitoring:

```yaml
title: Suspicious File Access
id: file-access-monitor
description: Monitor for access to sensitive system files
logsource:
  category: agent-tool
detection:
  selection:
    tool: file_operation
    path|contains:
      - '/etc/passwd'
      - '/etc/shadow'
      - '.ssh/'
  condition: selection
level: medium
```

### Managing Rules

- Hot reload is enabled by default (`rules.hot_reload: true`)
- Manual reload: `agentshield rules reload` (sends SIGHUP)
- List loaded rules: `agentshield rules list`
- Rules repository: `agentshield-ai/sigma-ai`

## OpenClaw Integration

The plugin registers as `agentshield` in OpenClaw's plugin system. The installer patches OpenClaw config automatically:

```json
{
  "plugins": {
    "entries": {
      "agentshield": {
        "enabled": true,
        "config": {
          "enabled": true,
          "endpoint": "http://127.0.0.1:8433/api/v1/evaluate",
          "auth_token": "<generated-token>",
          "timeout_ms": 200,
          "timeout_policy": "block"
        }
      }
    }
  }
}
```

Plugin hooks registered:
- `before_tool_call` (priority -100): Synchronous evaluation with timeout
- `after_tool_call`: Fire-and-forget audit report
- `session_start`, `session_end`, `before_agent_start`, `agent_end`: Lifecycle events

See the [plugin README](../README.md) for full plugin documentation.

## Troubleshooting

### Engine Won't Start

```bash
# Check logs
journalctl --user -u agentshield-engine -n 50

# Run with verbose output
agentshield serve --config ~/.agentshield/config.yaml --verbose

# Test health endpoint
curl -s -H "Authorization: Bearer YOUR_TOKEN" http://127.0.0.1:8433/api/v1/health
```

### Common Issues

- **Auth token too short**: Must be 32+ characters. Regenerate with `openssl rand -hex 32`.
- **Port conflict**: Check `netstat -an | grep 8433`. Change `server.port` in config.
- **Rules not loading**: Verify `rules.dir` path exists and contains `.yml` files.
- **Triage timeouts**: Increase `triage.timeout_sec` or switch to `health_check_mode: "connectivity"`.

### Testing the Engine Directly

```bash
curl -X POST http://127.0.0.1:8433/api/v1/evaluate \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "event_id": "test-001",
    "tool_name": "exec",
    "command": "ls -la",
    "params": {"command": "ls -la"},
    "fields": {"tool": "exec", "command": "ls -la"}
  }'
```

## File Locations

| File | Path |
|------|------|
| Binary | `~/.agentshield/agentshield-engine` |
| Config | `~/.agentshield/config.yaml` |
| Rules | `~/.agentshield/rules/` |
| Database | `~/.agentshield/agentshield.db` |
| Systemd service | `~/.config/systemd/user/agentshield-engine.service` |
| Launchd plist | `~/Library/LaunchAgents/ai.agentshield.engine.plist` |

## Uninstallation

```bash
cd plugins/openclaw/skill
./uninstall.sh
```

This stops the service, removes `~/.agentshield/`, reverts OpenClaw plugin config, and cleans up PATH references.

## License

Apache 2.0
