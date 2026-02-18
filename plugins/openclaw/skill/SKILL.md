# AgentShield

**AI Agent Detection & Response (AADR)** — Real-time security monitoring with Sigma rules and LLM-powered triage

## What is AgentShield?

AgentShield is a security engine that monitors AI agent activities in real-time. It intercepts tool calls, evaluates them against Sigma security rules, and optionally uses LLM triage to assess threats. Think of it as an EDR (Endpoint Detection & Response) system, but for AI agents.

## Architecture

**Single Go Binary** — Zero dependencies, pure Go implementation:
- **agentshield-engine**: HTTP server, rule engine, SQLite store, LLM triage — all in one
- **No Python dependency** — unlike previous versions
- **No sidecar processes** — everything runs in a single binary
- **One systemd service** — `agentshield-engine.service`

## How It Works

1. **Interception**: OpenClaw routes tool calls through AgentShield
2. **Rule Evaluation**: Each call is evaluated against Sigma security rules
3. **LLM Triage** (optional): Suspicious events are analyzed by LLM for context-aware decisions
4. **Response**: Block, allow, or alert based on evaluation results

## Installation

### Quick Install

```bash
./install.sh
```

The installer will:
- Download the latest binary for your platform (or compile with Go)
- Set up `~/.agentshield/` directory structure
- Generate a secure authentication token
- Configure systemd service
- Patch OpenClaw configuration
- Start the engine

### Manual Installation

If you prefer manual setup:

```bash
# 1. Download binary
curl -L -o agentshield-engine https://github.com/agentshield-ai/agentshield/releases/latest/download/agentshield-linux-amd64
chmod +x agentshield-engine
mv agentshield-engine ~/.agentshield/

# 2. Create config (see Configuration section)
mkdir -p ~/.agentshield/rules
# ... create config.yaml ...

# 3. Download rules
git clone https://github.com/agentshield-ai/agentshield-rules.git ~/.agentshield/rules

# 4. Start the engine
~/.agentshield/agentshield-engine serve --config ~/.agentshield/config.yaml
```

## Configuration

Configuration file: `~/.agentshield/config.yaml`

```yaml
# Server settings
server:
  address: "127.0.0.1:8432"
  
# Authentication (MANDATORY)
auth:
  token: "your-32-plus-character-secure-token-here"
  
# Rule settings
rules_dir: "~/.agentshield/rules"

# Storage
store:
  path: "~/.agentshield/agentshield.db"
  
# Evaluation mode: enforce, monitor, disabled
evaluation_mode: "enforce"

# Logging
log_level: "info"

# LLM Triage (optional - requires API key)
triage:
  enabled: true
  provider: "openai"          # openai, anthropic, etc.
  model: "gpt-4"
  api_key: "your-api-key"
  max_tokens: 150
  temperature: 0.1
  timeout: "10s"
```

### Authentication

AgentShield requires a secure authentication token (32+ characters). The installer generates one automatically, but you can create your own:

```bash
# Using openssl
openssl rand -hex 32

# Using Python
python3 -c "import secrets; print(secrets.token_hex(32))"
```

## CLI Commands

```bash
# Start the server (usually done via systemd)
agentshield serve --config ~/.agentshield/config.yaml

# Check status
agentshield status

# List recent alerts
agentshield alerts

# Manage rules
agentshield rules list
agentshield rules validate /path/to/rule.yml
agentshield rules reload

# Analyze and refine rules
agentshield refine --rule-id suspicious-file-access

# Version info
agentshield version
```

### Service Management

```bash
# Systemd service (user level)
systemctl --user status agentshield-engine
systemctl --user start agentshield-engine
systemctl --user stop agentshield-engine
systemctl --user restart agentshield-engine

# View logs
journalctl --user -u agentshield-engine -f
```

## Enabling LLM Triage

LLM triage provides context-aware threat analysis by having an AI model evaluate suspicious events:

1. **Get API Key**: OpenAI, Anthropic, or your preferred provider
2. **Edit Config**: Uncomment and configure the `triage` section in `~/.agentshield/config.yaml`
3. **Restart Service**: `systemctl --user restart agentshield-engine`

Example triage configuration:

```yaml
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4"
  api_key: "sk-your-openai-key-here"
  max_tokens: 150
  temperature: 0.1
  system_prompt: "You are a security analyst. Evaluate this agent activity for threats."
```

## Security Rules

AgentShield uses Sigma rules adapted for AI agent monitoring. Rules are stored in `~/.agentshield/rules/`.

### Rule Format

```yaml
title: Suspicious File Access
id: file-access-monitor
description: Monitor for suspicious file operations
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

### Adding Custom Rules

1. Create a `.yml` file in `~/.agentshield/rules/`
2. Reload rules: `agentshield rules reload`
3. Test: `agentshield rules validate your-rule.yml`

### Rule Categories

- **File Operations**: Monitor suspicious file access patterns
- **Network Activity**: Detect unusual network connections
- **System Commands**: Flag dangerous shell commands
- **Data Exfiltration**: Watch for data theft patterns
- **Privilege Escalation**: Detect attempts to gain higher privileges

## OpenClaw Integration

AgentShield integrates with OpenClaw as a plugin. The installer automatically configures:

```json
{
  "plugins": {
    "agentshield": {
      "enabled": true,
      "endpoint": "http://127.0.0.1:8432/api/v1/evaluate",
      "auth_token": "your-generated-token",
      "timeout_ms": 100,
      "timeout_policy": "allow"
    }
  }
}
```

## Troubleshooting

### Service Won't Start

```bash
# Check logs
journalctl --user -u agentshield-engine -n 50

# Check config syntax
agentshield serve --config ~/.agentshield/config.yaml --verbose

# Test connectivity
curl -H "Authorization: Bearer YOUR-TOKEN" http://127.0.0.1:8432/api/v1/status
```

### High False Positives

1. **Tune Rules**: Edit rules in `~/.agentshield/rules/` to be more specific
2. **Use Monitor Mode**: Set `evaluation_mode: "monitor"` to log without blocking
3. **Enable LLM Triage**: More context-aware decisions reduce false positives

### Performance Issues

- **Reduce Timeout**: Lower `timeout_ms` in OpenClaw config
- **Disable Triage**: Comment out triage config for faster evaluation
- **Optimize Rules**: Remove overly broad rule patterns

### Integration Issues

```bash
# Test AgentShield directly
curl -X POST http://127.0.0.1:8432/api/v1/evaluate \
  -H "Authorization: Bearer YOUR-TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"tool": "test", "params": {}}'

# Check OpenClaw plugin config
openclaw config get plugins.agentshield
```

## File Locations

- **Binary**: `~/.agentshield/agentshield-engine`
- **Config**: `~/.agentshield/config.yaml`
- **Rules**: `~/.agentshield/rules/`
- **Database**: `~/.agentshield/agentshield.db`
- **PID File**: `~/.agentshield/agentshield.pid`
- **Service**: `~/.config/systemd/user/agentshield-engine.service`

## Uninstallation

```bash
./uninstall.sh
```

This will:
- Stop and disable the systemd service
- Remove all AgentShield files and configuration
- Revert OpenClaw plugin settings
- Clean up completely

## Feedback & Support

### Reporting Issues

Found a bug or need help? Please open an issue:
- **GitHub Issues**: https://github.com/agentshield-ai/agentshield/issues
- **Rule Issues**: https://github.com/agentshield-ai/agentshield-rules/issues

When reporting issues, include:
- AgentShield version (`agentshield version`)
- Operating system and architecture
- Configuration file (redact sensitive tokens)
- Relevant log entries

### Rule Contributions

Help improve security rules:
1. Fork the [agentshield-rules](https://github.com/agentshield-ai/agentshield-rules) repository
2. Add or improve rules
3. Test with `agentshield rules validate`
4. Submit a pull request

### Feature Requests

Have an idea for AgentShield? Open a feature request on GitHub with:
- Use case description
- Expected behavior
- Why it would be valuable

## License

AgentShield is open source under the Apache 2.0 License. See the full license in the [GitHub repository](https://github.com/agentshield-ai/agentshield).

---

**AgentShield** — Protecting AI agents, one tool call at a time. 🛡️