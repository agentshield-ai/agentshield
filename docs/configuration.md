# Configuration Guide

AgentShield can be configured via environment variables, a YAML configuration file, or a combination of both.

## Configuration Priority

Settings are applied in this order (highest to lowest priority):

1. **Environment variables** (e.g., `AGENTSHIELD_LOG_LEVEL`)
2. **YAML config file** (`~/.agentshield/config.yaml`)
3. **Default values**

## Environment Variables

### Required

| Variable | Description | Example |
|----------|-------------|---------|
| `ANTHROPIC_API_KEY` | API key for Claude LLM triage | `sk-ant-api03-...` |

Note: `ANTHROPIC_API_KEY` uses the standard Anthropic SDK environment variable name (not `AGENTSHIELD_ANTHROPIC_API_KEY`).

### Optional

All optional environment variables use the `AGENTSHIELD_` prefix:

| Variable | Description | Default |
|----------|-------------|---------|
| `AGENTSHIELD_LOG_LEVEL` | Log verbosity (DEBUG, INFO, WARNING, ERROR, CRITICAL) | `INFO` |
| `AGENTSHIELD_DATA_DIR` | Base data directory | `~/.agentshield` |
| `AGENTSHIELD_RULES_DIR` | Directory containing Sigma rules | `~/.agentshield/rules` |
| `AGENTSHIELD_DB_PATH` | SQLite database file path | `~/.agentshield/agentshield.db` |
| `AGENTSHIELD_CONFIG_PATH` | Custom config file path | `~/.agentshield/config.yaml` |

## YAML Configuration File

Create `~/.agentshield/config.yaml`:

```yaml
# Logging configuration
log_level: INFO

# Path settings
data_dir: ~/.agentshield
rules_dir: ~/.agentshield/rules
db_path: ~/.agentshield/agentshield.db

# Log paths to monitor (list of paths)
log_paths:
  - ~/.clawdbot/logs/agent.jsonl
  - ~/.claude-code/logs/agent.jsonl
```

## Configuration Settings

### log_level

Controls the verbosity of AgentShield's logging output.

Valid values: `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL`

```yaml
log_level: DEBUG  # Show all log messages
```

### data_dir

Base directory for AgentShield's data files (database, position tracking, etc.).

```yaml
data_dir: /var/lib/agentshield
```

### rules_dir

Directory containing Sigma detection rules. AgentShield loads all `.yml` and `.yaml` files from this directory.

```yaml
rules_dir: /etc/agentshield/rules
```

### db_path

Path to the SQLite database file. This stores events, alerts, and feedback.

```yaml
db_path: /var/lib/agentshield/data.db
```

### log_paths

List of log file paths to monitor. AgentShield watches these files for new events.

```yaml
log_paths:
  - ~/.clawdbot/logs/agent.jsonl
  - /var/log/ai-agent/events.jsonl
```

Default: `~/.clawdbot/logs/agent.jsonl`

## Example Configurations

### Minimal Configuration (Development)

```yaml
log_level: DEBUG
log_paths:
  - ~/.clawdbot/logs/agent.jsonl
```

### Production Configuration

```yaml
log_level: WARNING
data_dir: /var/lib/agentshield
rules_dir: /etc/agentshield/rules
db_path: /var/lib/agentshield/agentshield.db
log_paths:
  - /var/log/clawdbot/agent.jsonl
  - /var/log/claude-code/agent.jsonl
```

## Directory Structure

After installation, AgentShield uses this directory structure:

```
~/.agentshield/
├── config.yaml         # Configuration file
├── agentshield.db      # SQLite database
├── .positions.json     # Log file position tracking
└── rules/              # Sigma detection rules
    ├── agent_rce_injection.yml
    ├── agent_credential_access.yml
    └── ...
```

## Validating Configuration

Test your configuration by running:

```bash
agentshield status
```

This will load the configuration and show any errors.

## Troubleshooting

### "ANTHROPIC_API_KEY not set"

LLM triage requires an Anthropic API key. Set it:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

Without an API key, alerts will be marked as `SUSPICIOUS` instead of being triaged by the LLM.

### "Invalid log level"

Ensure `log_level` is one of: `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL`.

```yaml
log_level: INFO  # Correct
log_level: VERBOSE  # Invalid
```

### "Rules directory not found"

Create the rules directory and copy rules:

```bash
mkdir -p ~/.agentshield/rules
cp -r rules/* ~/.agentshield/rules/
```
