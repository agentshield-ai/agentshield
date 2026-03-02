# Configuration Reference

This document provides a complete reference for all AgentShield Engine configuration options, including field types, default values, and environment variable overrides.

## Configuration File

The engine uses YAML configuration with environment variable substitution (e.g. `${AGENTSHIELD_AUTH_TOKEN}`). Configuration files are searched in the following order; the first file found is used:

1. `./config.yaml` (working directory)
2. `~/.agentshield/config.yaml` (user configuration)
3. `/etc/agentshield/config.yaml` (system configuration)

A specific path may be provided with the `--config` flag.

## Full Example Configuration

```yaml
# HTTP Server Configuration
server:
  addr: "0.0.0.0"                    # Bind address (default: "127.0.0.1")
  port: 8433                         # HTTP port (default: 8433)

# Authentication Configuration
auth:
  token: "${AGENTSHIELD_AUTH_TOKEN}" # API authentication token (min 32 chars)

# Rules Configuration
rules:
  dir: "./rules"                     # Rules directory (default: "./rules")
  hot_reload: true                   # Enable hot reload on SIGHUP (default: true)

# Storage Configuration
store:
  sqlite_path: "./agentshield.db"    # SQLite database path (default: "./agentshield.db")

# Evaluation Configuration
evaluation_mode: "audit"             # Mode: enforce, audit, shadow (default: "enforce")

# Logging Configuration
log_level: "info"                    # Level: debug, info, warn, error (default: "info")

# Fast Triage Configuration (synchronous, ~4 seconds)
triage:
  enabled: true                      # Enable fast triage (default: false)
  provider: "openai"                 # Provider: openai, anthropic (default: "openai")
  model: "gpt-4o-mini"              # Model name (default: "gpt-4o-mini")
  api_key: "${OPENAI_API_KEY}"      # API key (env override available)
  base_url: ""                      # Custom base URL for OpenRouter, etc (optional)
  max_tokens: 500                   # Max response tokens (default: 500)
  timeout_sec: 10                   # Request timeout seconds (default: 10)
  health_check_mode: "full"         # "full" (default) or "connectivity"

# Deep Triage Configuration (async OpenClaw sub-agent)
deep_triage:
  enabled: true                      # Enable deep triage (default: false)
  gateway_url: "http://127.0.0.1:18789" # OpenClaw gateway URL (default: "http://127.0.0.1:18789")
  gateway_token: "${OPENCLAW_GATEWAY_TOKEN}" # Gateway auth token (env override available)
  min_severity: "critical"           # Minimum severity to trigger: low, medium, high, critical (default: "critical")
  webhook: ""                       # Optional webhook URL for results (optional)
  
  # Agent Configuration
  agent:
    system_prompt: |                 # Custom SOC analyst prompt (default: built-in)
      You are an expert cybersecurity analyst specialising in AI agent security.
      
      Your task is to analyse security alerts and determine if they represent 
      true threats or false positives. Consider:
      
      1. Context: Recent events and patterns
      2. Intent: Whether the action appears malicious or legitimate
      3. Impact: Potential damage if the alert is a true positive
      4. Environment: Normal vs. suspicious behaviour patterns
      
      Use available tools to gather additional context when needed.
      Provide clear reasoning for your verdict with confidence score.
    
    model: "anthropic/claude-sonnet-4-20250514" # Model for agent (optional override)
    thinking: "low"                  # Thinking mode: off, low, high (default: "off")
    tools:                          # Available tools (default: basic set)
      - "web_search"                # Web search capability
      - "web_fetch"                 # Fetch web content
      - "memory_search"             # Search agent memory
    timeout_sec: 60                 # Agent timeout seconds (default: 60)
```

## Configuration Sections

### Server Configuration

```yaml
server:
  addr: "0.0.0.0"    # Bind address
  port: 8433         # HTTP port
```

**Fields:**
- `addr` (string): IP address to bind to. Use "0.0.0.0" for all interfaces, "127.0.0.1" for localhost only
- `port` (int): HTTP port number. Avoid privileged ports (<1024) unless running as root

**Environment Overrides:**
- `AGENTSHIELD_ADDR`
- `AGENTSHIELD_PORT`

**Defaults:**
- `addr`: "127.0.0.1"
- `port`: 8433

**TLS note:** AgentShield does not terminate TLS. When using a non-localhost bind address (anything other than `127.0.0.1`, `localhost`, or `::1`), one should place a TLS-terminating reverse proxy (e.g. nginx, Caddy) in front of the engine.

### Authentication Configuration

```yaml
auth:
  token: "your-secret-token"
```

**Fields:**
- `token` (string): Bearer token for API authentication. If empty, authentication is disabled

**Environment Overrides:**
- `AGENTSHIELD_AUTH_TOKEN`

**Defaults:**
- `token`: "" (authentication disabled)

**Security note:** Always use strong, randomly generated tokens in production. Token comparison uses `subtle.ConstantTimeCompare` to mitigate timing attacks.

### Rules Configuration

```yaml
rules:
  dir: "./rules"
  hot_reload: true
```

**Fields:**
- `dir` (string): Directory containing Sigma rule files (.yml/.yaml)
- `hot_reload` (bool): Enable automatic rule reloading on SIGHUP signal

**Environment Overrides:**
- `AGENTSHIELD_RULES_DIR`

**Defaults:**
- `dir`: "./rules"
- `hot_reload`: true

### Storage Configuration

```yaml
store:
  sqlite_path: "./agentshield.db"
```

**Fields:**
- `sqlite_path` (string): Path to SQLite database file. Will be created if it doesn't exist

**Environment Overrides:**
- `AGENTSHIELD_DB_PATH`

**Defaults:**
- `sqlite_path`: "./agentshield.db"

**Note:** The engine automatically creates tables and handles migrations. Ensure the parent directory is writable.

### Evaluation Mode Configuration

```yaml
evaluation_mode: "audit"
```

**Values:**
- `enforce`: Block matching events and return action: "block"
- `audit`: Log matching events but don't block, return action: "log"  
- `shadow`: Silent monitoring, process events but don't affect response

**Environment Overrides:**
- `AGENTSHIELD_MODE`

**Defaults:**
- `evaluation_mode`: "enforce"

### Logging Configuration

```yaml
log_level: "info"
```

**Values:**
- `debug`: Verbose debug information
- `info`: General information messages
- `warn`: Warning conditions
- `error`: Error conditions only

**Environment Overrides:**
- `AGENTSHIELD_LOG_LEVEL`

**Defaults:**
- `log_level`: "info"

### Fast Triage Configuration

Fast triage provides synchronous LLM analysis (typically around 4 seconds) for high-priority alerts. See [Triage System](triage.md) for a full discussion of the two-tier triage architecture.

```yaml
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"
  base_url: "https://openrouter.ai/api/v1"
  max_tokens: 500
  timeout_sec: 10
  health_check_mode: "connectivity"  # "full" (default) or "connectivity"
```

**Fields:**
- `enabled` (bool): Enable fast triage system
- `provider` (string): LLM provider - "openai" or "anthropic"
- `model` (string): Model name (provider-specific)
- `api_key` (string): API key for external providers
- `base_url` (string): Custom API base URL (useful for OpenRouter)
- `max_tokens` (int): Maximum response tokens
- `timeout_sec` (int): Request timeout in seconds
- `health_check_mode` (string): Health check strategy - "full" or "connectivity" (default: "full")

**Health Check Modes:**
- `full` (default): Makes a minimal completion API call to verify end-to-end functionality. Costs a small number of tokens per check.
- `connectivity`: Validates API key and network connectivity without spending tokens. For OpenAI, uses the free `/v1/models` endpoint. For Anthropic, uses a minimal request (`max_tokens=1`, single-char prompt) since no free endpoint is available.

Use `connectivity` mode in development or high-frequency health check scenarios to reduce token costs.

**Provider Models:**
- **OpenAI**: "gpt-4o", "gpt-4o-mini", "gpt-4-turbo"
- **Anthropic**: "claude-3-5-sonnet-20241022", "claude-3-haiku-20240307"
- **OpenRouter**: Any model via OpenAI-compatible `base_url`

**Environment Overrides:**
- `AGENTSHIELD_TRIAGE_API_KEY` (only the API key is overridable via env; other triage fields require YAML config)

**Defaults:**
- `enabled`: false
- `provider`: "openai"
- `model`: "gpt-4o-mini"
- `max_tokens`: 500
- `timeout_sec`: 10
- `health_check_mode`: "full"

### Deep Triage Configuration

Deep triage uses asynchronous OpenClaw sub-agents with tool access for comprehensive analysis.

```yaml
deep_triage:
  enabled: true
  gateway_url: "http://127.0.0.1:18789"
  gateway_token: "${OPENCLAW_GATEWAY_TOKEN}"
  min_severity: "critical"
  webhook: "https://your-soc.com/webhook"
  
  agent:
    system_prompt: |
      Your custom SOC analyst prompt here...
    model: "anthropic/claude-sonnet-4-20250514"
    thinking: "low"
    tools:
      - "web_search"
      - "web_fetch"
      - "memory_search"
    timeout_sec: 60
```

**Fields:**
- `enabled` (bool): Enable deep triage system
- `gateway_url` (string): OpenClaw gateway endpoint URL
- `gateway_token` (string): Gateway authentication token
- `min_severity` (string): Minimum alert severity to trigger deep triage
- `webhook` (string): Optional webhook URL for async result delivery

**Agent Fields:**
- `system_prompt` (string): Custom system prompt for the analysis agent
- `model` (string): Model override for the agent (OpenClaw format)
- `thinking` (string): Thinking mode - "off", "low", "high"
- `tools` ([]string): Available tools for the agent
- `timeout_sec` (int): Agent session timeout

**Severity Levels:**
- `low`: All alerts trigger deep triage
- `medium`: Medium and higher alerts
- `high`: High and critical alerts only  
- `critical`: Critical alerts only

**Available Tools:**
- `web_search`: Web search via Brave API
- `web_fetch`: Fetch and parse web content
- `memory_search`: Search through agent memory/context

**Environment Overrides:**
- `OPENCLAW_GATEWAY_TOKEN` (only the gateway token is overridable via env; other deep-triage fields require YAML config)

**Defaults:**
- `enabled`: false
- `gateway_url`: "http://127.0.0.1:18789"
- `min_severity`: "critical"
- `agent.thinking`: "off"
- `agent.timeout_sec`: 60

## Configuration Examples

### Minimal Configuration

```yaml
# Minimal config for basic rule evaluation
server:
  port: 8433
rules:
  dir: "./rules"
evaluation_mode: "audit"
```

### OpenRouter Integration

```yaml
# Use OpenRouter for cost-effective LLM access
triage:
  enabled: true
  provider: "openai"
  model: "anthropic/claude-3.5-sonnet"
  api_key: "${OPENROUTER_API_KEY}"
  base_url: "https://openrouter.ai/api/v1"
```

### Production Configuration

```yaml
# Production-ready configuration
server:
  addr: "0.0.0.0"
  port: 8433

auth:
  token: "${AGENTSHIELD_AUTH_TOKEN}"

rules:
  dir: "/etc/agentshield/rules"
  hot_reload: true

store:
  sqlite_path: "/var/lib/agentshield/alerts.db"

evaluation_mode: "enforce"
log_level: "warn"

triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"
  timeout_sec: 15

deep_triage:
  enabled: true
  gateway_token: "${OPENCLAW_GATEWAY_TOKEN}"
  min_severity: "high"
  webhook: "https://soc.company.com/webhooks/agentshield"
  
  agent:
    thinking: "low"
    tools: ["web_search", "web_fetch"]
    timeout_sec: 120
```

### Development Configuration

```yaml
# Development configuration with debug logging
server:
  addr: "127.0.0.1"
  port: 8434

rules:
  dir: "./test-rules"
  hot_reload: true

store:
  sqlite_path: "./dev-alerts.db"

evaluation_mode: "shadow"
log_level: "debug"

triage:
  enabled: true
  provider: "openai"
  model: "anthropic/claude-sonnet-4-20250514"
  base_url: "https://openrouter.ai/api/v1"
  api_key: "${OPENROUTER_API_KEY}"
```

## Environment Variable Reference

The following environment variables are implemented as overrides in `applyEnvOverrides()`:

| Environment Variable | Configuration Path | Type | Description |
|---------------------|-------------------|------|-------------|
| `AGENTSHIELD_ADDR` | `server.addr` | string | Server bind address |
| `AGENTSHIELD_PORT` | `server.port` | int | Server port |
| `AGENTSHIELD_AUTH_TOKEN` | `auth.token` | string | API auth token |
| `AGENTSHIELD_RULES_DIR` | `rules.dir` | string | Rules directory |
| `AGENTSHIELD_DB_PATH` | `store.sqlite_path` | string | SQLite path |
| `AGENTSHIELD_MODE` | `evaluation_mode` | string | Evaluation mode |
| `AGENTSHIELD_LOG_LEVEL` | `log_level` | string | Log level |
| `AGENTSHIELD_TRIAGE_API_KEY` | `triage.api_key` | string | Triage API key |
| `OPENCLAW_GATEWAY_TOKEN` | `deep_triage.gateway_token` | string | Deep triage gateway token |

Other triage and deep-triage fields (provider, model, base_url, etc.) are set only via the YAML config file.

## Validation

The engine validates configuration on startup. To see validation errors, run with debug logging:

```bash
AGENTSHIELD_LOG_LEVEL=debug ./agentshield serve --config config.yaml
```

**Common Validation Errors:**
- Invalid evaluation mode
- Missing rules directory
- Invalid log level
- Missing API keys when triage is enabled
- Invalid webhook URLs
- Port conflicts

## Configuration Management

### Hot Reloading

Rules can be reloaded without restarting:

```bash
# Send SIGHUP signal for rule reload
kill -HUP $(pgrep agentshield)

# Or use CLI
./agentshield rules reload
```

**Note:** Only rules are hot-reloadable; the verdict cache is automatically invalidated on reload. Server configuration changes require a restart.

### Multiple Environments

Use environment-specific configuration files:

```bash
# Development
./agentshield serve --config configs/development.yaml

# Production
./agentshield serve --config configs/production.yaml

# With environment overrides
AGENTSHIELD_LOG_LEVEL=debug ./agentshield serve --config production.yaml
```

### Docker Configuration

```dockerfile
# Mount configuration as volume
COPY config.yaml /etc/agentshield/config.yaml
VOLUME ["/etc/agentshield/rules"]

# Use environment variables
ENV AGENTSHIELD_AUTH_TOKEN=${AUTH_TOKEN}
ENV AGENTSHIELD_LOG_LEVEL=info
```