# Deployment Guide

This guide covers installation, daemon setup, transport security, and integration scenarios for the AgentShield engine. It is intended for operators deploying AgentShield in development, staging, or production environments.

## Installation Methods

### Go Install (Recommended)

Install directly from source using the Go toolchain:

```bash
# Install latest version
go install github.com/agentshield-ai/agentshield/cmd/agentshield@latest

# Verify installation
agentshield version
```

**Requirements:**
- Go 1.24 or later
- Git (for fetching dependencies)

### Build from Source

For development or custom builds:

```bash
# Clone repository with sigmalite subtree
git clone --recursive https://github.com/agentshield-ai/agentshield.git
cd agentshield

# Build binary
go build -o agentshield ./cmd/agentshield/

# Optional: Install to $GOPATH/bin
go install ./cmd/agentshield/

# Verify build
./agentshield version
```

### Binary Releases

Download pre-built binaries from GitHub releases:

```bash
# Download for Linux x64
curl -L https://github.com/agentshield-ai/agentshield/releases/latest/download/agentshield-linux-amd64.tar.gz | tar xz

# Download for macOS
curl -L https://github.com/agentshield-ai/agentshield/releases/latest/download/agentshield-darwin-amd64.tar.gz | tar xz

# Download for Windows
curl -L https://github.com/agentshield-ai/agentshield/releases/latest/download/agentshield-windows-amd64.zip -o agentshield.zip
```

### Docker Installation

> **Note:** Official Docker images are not yet published to a public registry. For now, build a local image from source:

```bash
# Build a local Docker image
make docker-build    # produces agentshield-engine:test

# Run integration tests against the Docker image
make test-integration-docker
```

Once official images are available, the expected usage will be:

```bash
# docker pull agentshield/engine:latest
#
# docker run -d \
#   --name agentshield \
#   -p 8433:8433 \
#   -v /path/to/rules:/etc/rules:ro \
#   -v /path/to/config.yaml:/etc/agentshield/config.yaml:ro \
#   -v agentshield-data:/var/lib/agentshield \
#   agentshield/engine:latest
```

## Transport Security (TLS)

AgentShield does **not** handle TLS natively. Production deployments that expose the engine beyond localhost **must** use a TLS-terminating reverse proxy such as nginx, Caddy, or a cloud load balancer.

### Example: nginx reverse proxy with TLS

```nginx
server {
    listen 443 ssl;
    server_name agentshield.example.com;

    ssl_certificate     /etc/ssl/certs/agentshield.pem;
    ssl_certificate_key /etc/ssl/private/agentshield.key;

    location / {
        proxy_pass http://127.0.0.1:8433;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

When running behind a reverse proxy, bind AgentShield to `127.0.0.1` (the default) so it is not directly reachable from the network. The engine will log a warning at startup if a non-localhost bind address is detected.

## Configuration Setup

### Directory Structure

Create standard directory structure:

```bash
# System installation
sudo mkdir -p /etc/agentshield/{rules,config.d}
sudo mkdir -p /var/lib/agentshield
sudo mkdir -p /var/log/agentshield

# User installation  
mkdir -p ~/.agentshield/{rules,config.d}
mkdir -p ~/.local/share/agentshield/logs
```

### Basic Configuration

Create configuration file:

```bash
# System config
sudo tee /etc/agentshield/config.yaml << 'EOF'
server:
  addr: "127.0.0.1"   # Bind to localhost; use a reverse proxy for external access
  port: 8433

auth:
  token: "${AGENTSHIELD_AUTH_TOKEN}"

rules:
  dir: "/etc/agentshield/rules"
  hot_reload: true

store:
  sqlite_path: "/var/lib/agentshield/alerts.db"

evaluation_mode: "audit"
log_level: "info"

triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"

deep_triage:
  enabled: false  # Enable after OpenClaw setup
EOF

# User config
tee ~/.agentshield/config.yaml << 'EOF'
server:
  addr: "127.0.0.1"
  port: 8434

rules:
  dir: "~/.agentshield/rules"
  hot_reload: true

store:
  sqlite_path: "~/.local/share/agentshield/alerts.db"

evaluation_mode: "shadow"
log_level: "debug"
EOF
```

### Environment Variables

Set up an environment file for secrets. **Do not commit this file to version control.**

```bash
# Create environment file
sudo tee /etc/agentshield/environment << 'EOF'
AGENTSHIELD_AUTH_TOKEN=<generate-a-secure-random-token>
OPENAI_API_KEY=sk-<your-openai-key>
OPENCLAW_GATEWAY_TOKEN=<your-openclaw-token>
AGENTSHIELD_LOG_LEVEL=info
EOF

# Restrict read access to root and the agentshield service user
sudo chmod 600 /etc/agentshield/environment
sudo chown root:agentshield /etc/agentshield/environment
```

The systemd unit file (below) references this file via `EnvironmentFile`, so there is no need to source it in a shell profile.

## Daemon Setup

### Systemd Service (Linux)

Create systemd service for production deployment:

```bash
# Create service file
sudo tee /etc/systemd/system/agentshield.service << 'EOF'
[Unit]
Description=AgentShield Detection Engine
Documentation=https://github.com/agentshield-ai/agentshield
After=network.target
Wants=network.target

[Service]
Type=simple
User=agentshield
Group=agentshield
ExecStart=/usr/local/bin/agentshield serve --config /etc/agentshield/config.yaml
ExecReload=/bin/kill -HUP $MAINPID
EnvironmentFile=-/etc/agentshield/environment
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=agentshield

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/agentshield /var/log/agentshield
CapabilityBoundingSet=
AmbientCapabilities=
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictRealtime=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
EOF

# Create user and group
sudo useradd -r -s /bin/false -d /var/lib/agentshield agentshield
sudo chown -R agentshield:agentshield /var/lib/agentshield
sudo chown -R agentshield:agentshield /var/log/agentshield

# Enable and start service
sudo systemctl daemon-reload
sudo systemctl enable agentshield
sudo systemctl start agentshield

# Check status
sudo systemctl status agentshield
```

### Launchd Service (macOS)

Create launchd plist for macOS:

```bash
# Create plist file
sudo tee /Library/LaunchDaemons/com.agentshield.engine.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.agentshield.engine</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/agentshield</string>
        <string>serve</string>
        <string>--config</string>
        <string>/usr/local/etc/agentshield/config.yaml</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>AGENTSHIELD_AUTH_TOKEN</key>
        <string>your-token</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/agentshield/stdout.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/agentshield/stderr.log</string>
</dict>
</plist>
EOF

# Load and start service
sudo launchctl load /Library/LaunchDaemons/com.agentshield.engine.plist
sudo launchctl start com.agentshield.engine
```

### Windows Service

Create Windows service using NSSM or sc:

```cmd
REM Download NSSM (Non-Sucking Service Manager)
REM Install as service
nssm install AgentShield "C:\Program Files\AgentShield\agentshield.exe"
nssm set AgentShield Parameters "serve --config C:\ProgramData\AgentShield\config.yaml"
nssm set AgentShield DisplayName "AgentShield Detection Engine"
nssm set AgentShield Description "AI agent security monitoring service"
nssm set AgentShield Start SERVICE_AUTO_START

REM Start service
nssm start AgentShield
```

### Manual Daemon Mode

Run manually with daemon features:

```bash
# Start with PID file
agentshield serve --config config.yaml --daemon --pid-file /var/run/agentshield.pid

# Reload rules (SIGHUP)
kill -HUP $(cat /var/run/agentshield.pid)

# Graceful shutdown (SIGTERM)
kill -TERM $(cat /var/run/agentshield.pid)

# Check if running
ps aux | grep agentshield
```

## Integration Deployments

### OpenClaw Plugin Integration

Configure AgentShield as an OpenClaw plugin:

```yaml
# In OpenClaw config.yaml
plugins:
  agentshield:
    enabled: true
    endpoint: "http://localhost:8433/api/v1/evaluate"
    token: "${AGENTSHIELD_AUTH_TOKEN}"
    timeout_sec: 30
    retry_attempts: 3
    retry_delay_sec: 1
    
    # Evaluation settings
    mode: "audit"  # enforce, audit, shadow
    
    # Field mapping
    field_mapping:
      tool: "action.name"
      command: "process.command_line"
      path: "file.path"
      url: "url.full"
      
    # Error handling
    on_error: "allow"  # allow, block, fail
    fallback_mode: "shadow"
```

**OpenClaw Installation Script:**

```bash
#!/bin/bash
# Install AgentShield for OpenClaw

# Install engine
go install github.com/agentshield-ai/agentshield/cmd/agentshield@latest

# Create config directory
mkdir -p ~/.openclaw/plugins/agentshield

# Create config
cat > ~/.openclaw/plugins/agentshield/config.yaml << 'EOF'
server:
  addr: "127.0.0.1"
  port: 8433

rules:
  dir: "~/.openclaw/plugins/agentshield/rules"
  hot_reload: true

store:
  sqlite_path: "~/.openclaw/plugins/agentshield/alerts.db"

evaluation_mode: "audit"

triage:
  enabled: true
  provider: "openai"
  model: "anthropic/claude-sonnet-4-5-20250929"
  base_url: "https://openrouter.ai/api/v1"
  api_key: "${OPENROUTER_API_KEY}"

deep_triage:
  enabled: true
  gateway_url: "http://127.0.0.1:18789"
EOF

# Copy bundled rules
cp -r ./rules ~/.openclaw/plugins/agentshield/rules

# Start engine
agentshield serve --config ~/.openclaw/plugins/agentshield/config.yaml &

echo "AgentShield engine running on port 8433"
```

### Claude Code / Other Agent Integration

AgentShield exposes a standard HTTP API. Integrations submit events to `POST /api/v1/evaluate` and act on the returned `action` field. See [api.md](api.md) for the full contract.

### Codex Integration

Configure for GitHub Copilot/Codex environments:

```bash
# Create Codex-specific rules
mkdir -p ~/.codex/security/rules

# Codex configuration
cat > ~/.codex/security/config.yaml << 'EOF'
server:
  addr: "127.0.0.1"
  port: 8434  # Different port to avoid conflicts

rules:
  dir: "~/.codex/security/rules"

evaluation_mode: "shadow"  # Monitor without blocking initially

triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"
EOF

# Start monitoring
agentshield serve --config ~/.codex/security/config.yaml &
```

## Standalone Usage Scenarios

### Security Research Environment

```bash
# High-verbosity monitoring suitable for research and red-team evaluation
cat > research-config.yaml << 'EOF'
server:
  addr: "0.0.0.0"
  port: 8433

evaluation_mode: "shadow"  # Monitor everything
log_level: "debug"

triage:
  enabled: true
  provider: "anthropic"
  model: "claude-sonnet-4-5-20250929"
  
deep_triage:
  enabled: true
  min_severity: "medium"  # Investigate more alerts
  agent:
    thinking: "high"  # Maximum analysis detail
    tools: ["web_search", "web_fetch"]
    timeout_sec: 300  # 5-minute investigations
EOF

# Start with verbose logging
AGENTSHIELD_LOG_LEVEL=debug agentshield serve --config research-config.yaml
```

### Production SOC Integration

```bash
# Production configuration with enforcement enabled
cat > production-config.yaml << 'EOF'
server:
  addr: "0.0.0.0"
  port: 8433

auth:
  token: "${AGENTSHIELD_AUTH_TOKEN}"

evaluation_mode: "enforce"  # Block threats
log_level: "warn"

triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"  # Fast and cost-effective
  timeout_sec: 5       # Quick decisions
  
deep_triage:
  enabled: true
  min_severity: "critical"  # Only highest priority
  webhook: "https://soc.company.com/api/webhooks/agentshield"
  agent:
    thinking: "low"
    timeout_sec: 60
EOF

# Deploy with monitoring
agentshield serve --config production-config.yaml 2>&1 | logger -t agentshield
```

## Environment Variables Reference

### Core Configuration

| Variable | Purpose | Default | Example |
|----------|---------|---------|---------|
| `AGENTSHIELD_AUTH_TOKEN` | API authentication | (required) | `abc123...` (min 32 chars) |
| `AGENTSHIELD_LOG_LEVEL` | Logging level | `info` | `debug` |
| `AGENTSHIELD_ADDR` | Bind address | `127.0.0.1` | `0.0.0.0` |
| `AGENTSHIELD_PORT` | HTTP port | `8433` | `8080` |

### Triage Configuration

| Variable | Purpose | Default | Example |
|----------|---------|---------|---------|
| `OPENAI_API_KEY` | OpenAI API key | None | `sk-...` |
| `ANTHROPIC_API_KEY` | Anthropic API key | None | `ant_...` |
| `OPENROUTER_API_KEY` | OpenRouter API key | None | `sk-or-...` |
| `OPENCLAW_GATEWAY_TOKEN` | OpenClaw token | None | `gw_...` |

### System Configuration

| Variable | Purpose | Default | Example |
|----------|---------|---------|---------|
| `AGENTSHIELD_RULES_DIR` | Rules directory | `./rules` | `/etc/agentshield/rules` |
| `AGENTSHIELD_DB_PATH` | Database path | `./agentshield.db` | `/var/lib/agentshield/alerts.db` |
| `AGENTSHIELD_MODE` | Evaluation mode | `enforce` | `audit` |
| `AGENTSHIELD_TRIAGE_API_KEY` | Triage LLM API key | None | `sk-...` |

## Health Monitoring

### Basic Health Checks

```bash
# HTTP health check (no auth required)
curl -f http://localhost:8433/api/v1/health

# Process check
pgrep -f agentshield
```

### Monitoring Scripts

Create monitoring scripts for production:

```bash
#!/bin/bash
# /usr/local/bin/agentshield-monitor.sh

HEALTH_URL="http://localhost:8433/api/v1/health"
PID_FILE="/var/run/agentshield.pid"
MAX_RESPONSE_TIME=5

# Check if process is running
if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if ! kill -0 "$PID" 2>/dev/null; then
        echo "CRITICAL: AgentShield process not running"
        exit 2
    fi
else
    echo "CRITICAL: PID file not found"
    exit 2
fi

# Check HTTP health
RESPONSE=$(curl -s -w "%{http_code}:%{time_total}" -m "$MAX_RESPONSE_TIME" "$HEALTH_URL" 2>/dev/null)
HTTP_CODE=$(echo "$RESPONSE" | cut -d: -f1)
RESPONSE_TIME=$(echo "$RESPONSE" | cut -d: -f2)

if [ "$HTTP_CODE" != "200" ]; then
    echo "CRITICAL: Health check failed (HTTP $HTTP_CODE)"
    exit 2
fi

if (( $(echo "$RESPONSE_TIME > $MAX_RESPONSE_TIME" | bc -l) )); then
    echo "WARNING: Slow response time (${RESPONSE_TIME}s)"
    exit 1
fi

echo "OK: AgentShield healthy (${RESPONSE_TIME}s)"
exit 0
```

### Log Monitoring

Set up log monitoring for errors:

```bash
# Monitor for errors
tail -f /var/log/agentshield/engine.log | grep -E "(ERROR|FATAL)"

# Monitor triage performance
tail -f /var/log/agentshield/engine.log | grep "triage" | \
  jq -r '. | "\(.timestamp) \(.level) \(.msg)"'

# Alert on high error rate
tail -f /var/log/agentshield/engine.log | \
  grep -E "(ERROR|FATAL)" | \
  while read line; do
    echo "$(date): ALERT - $line" | mail -s "AgentShield Error" ops@company.com
  done
```

## Performance Tuning

### Resource Limits

Configure appropriate resource limits:

```bash
# Systemd service limits
[Service]
MemoryMax=1G
MemoryHigh=512M
TasksMax=100
CPUQuota=200%

# Container limits  
docker run --memory=1g --cpus=2 agentshield/engine:latest

# Manual limits (ulimit)
ulimit -m 1048576  # 1GB memory
ulimit -u 100      # 100 processes
```

### Database Maintenance

SQLite WAL mode is used automatically. For periodic vacuuming, use the SQLite CLI directly:

```bash
sqlite3 /var/lib/agentshield/alerts.db "VACUUM;"
```

### Network Performance

```bash
# Tune network settings for high throughput
echo 'net.core.somaxconn = 65535' >> /etc/sysctl.conf
echo 'net.ipv4.tcp_max_syn_backlog = 65535' >> /etc/sysctl.conf
sysctl -p
```

## Troubleshooting

### Common Issues

**Issue**: "Permission denied" on startup
```bash
# Fix: Check file permissions
chmod +x /usr/local/bin/agentshield
chown agentshield:agentshield /var/lib/agentshield
```

**Issue**: "Port already in use"
```bash
# Fix: Check what's using the port
sudo lsof -i :8433
# Change port or kill conflicting process
```

**Issue**: Rules not loading
```bash
# Fix: Check rules directory and permissions
ls -la /etc/agentshield/rules/
# List rules that the engine can load
agentshield rules list
```

### Debug Mode

Enable debug logging:

```bash
AGENTSHIELD_LOG_LEVEL=debug agentshield serve --config config.yaml
```

### Log Analysis

Analyze logs for issues:

```bash
# Find errors in logs
grep -E "(ERROR|FATAL)" /var/log/agentshield/engine.log | tail -20

# Check triage failures
grep "triage.*failed" /var/log/agentshield/engine.log

# Monitor resource usage
grep "memory\|cpu" /var/log/agentshield/engine.log
```

Select the installation method and configuration that best suits your environment and security requirements. For further details, see [configuration.md](configuration.md) for the full config reference and [log_rotation.md](log_rotation.md) for data retention.