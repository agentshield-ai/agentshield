# Log Rotation and Glob Pattern Support

AgentShield automatically monitors log files that rotate daily or when new sessions start.

## Features

- ✅ **Glob pattern support** - Use wildcards to match multiple files
- ✅ **Automatic detection** - New files are detected and monitored automatically
- ✅ **Date-based rotation** - Handles daily log files (e.g., `clawdbot-2026-01-25.log`)
- ✅ **Session tracking** - Monitors new session files as they're created
- ✅ **No restart needed** - New files are picked up while AgentShield runs

## Configuration

### Glob Patterns

Use standard glob patterns in `~/.agentshield/config.yaml`:

```yaml
log_paths:
  # Monitor ALL Clawdbot sessions
  - ~/.clawdbot/agents/main/sessions/*.jsonl

  # Monitor daily rotated logs
  - /tmp/clawdbot/clawdbot-*.log

  # Monitor all Claude Code sessions for a specific project
  - ~/.claude/projects/-Users-yourname-project-name/*.jsonl

  # Monitor specific file (no wildcards)
  - /var/log/agent/specific.log
```

### Supported Patterns

| Pattern | Description | Example Matches |
|---------|-------------|-----------------|
| `*.jsonl` | All JSONL files | `session1.jsonl`, `session2.jsonl` |
| `clawdbot-*.log` | Date-stamped logs | `clawdbot-2026-01-25.log` |
| `**/sessions/*.jsonl` | Recursive search | `agents/main/sessions/abc.jsonl` |
| `session-????.jsonl` | Fixed-length wildcard | `session-2026.jsonl` |

## How It Works

### File Detection Interval

AgentShield checks for new files every 60 seconds by default. Configure this:

```python
daemon = MonitorDaemon(
    log_paths=[...],
    file_check_interval=60.0,  # Check every 60 seconds
)
```

### Automatic Monitoring

1. **Startup**: AgentShield expands all glob patterns and starts monitoring matching files
2. **Runtime**: Every `file_check_interval`, it re-expands patterns
3. **New files**: Automatically added to monitoring with position tracking
4. **Removed files**: Collectors gracefully removed

### Position Tracking

Each log file maintains its own position in `~/.agentshield/.positions.json`:

```json
{
  "/tmp/clawdbot/clawdbot-2026-01-25.log": 12345,
  "/tmp/clawdbot/clawdbot-2026-01-26.log": 0,
  "~/.clawdbot/agents/main/sessions/abc.jsonl": 54321
}
```

When a new file appears:
- Position starts at 0 (beginning of file)
- AgentShield reads all events from the new file
- Position is saved after each poll

## Use Cases

### Daily Log Rotation

**Clawdbot creates daily logs:**
```
/tmp/clawdbot/
├── clawdbot-2026-01-24.log  (yesterday)
├── clawdbot-2026-01-25.log  (today - being monitored)
└── clawdbot-2026-01-26.log  (tomorrow - auto-detected at midnight)
```

**Config:**
```yaml
log_paths:
  - /tmp/clawdbot/clawdbot-*.log
```

At midnight, AgentShield automatically:
1. Detects the new `clawdbot-2026-01-26.log`
2. Creates a new collector for it
3. Starts monitoring from position 0
4. Continues monitoring old file (in case of late writes)

### Session-Based Monitoring

**Clawdbot creates new session file per conversation:**
```
~/.clawdbot/agents/main/sessions/
├── session-abc123.jsonl  (session 1)
├── session-def456.jsonl  (session 2 - active)
└── session-ghi789.jsonl  (session 3 - new!)
```

**Config:**
```yaml
log_paths:
  - ~/.clawdbot/agents/main/sessions/*.jsonl
```

When a new session starts, AgentShield automatically monitors it.

### Multi-Project Monitoring

**Monitor Claude Code sessions for multiple projects:**
```yaml
log_paths:
  # Project 1
  - ~/.claude/projects/-Users-you-project1/*.jsonl

  # Project 2
  - ~/.claude/projects/-Users-you-project2/*.jsonl

  # All Clawdbot sessions
  - ~/.clawdbot/agents/*/sessions/*.jsonl
```

## Logs and Debugging

Check what files are being monitored:

```bash
# AgentShield startup shows expanded patterns
uv run agentshield start -f

# Output:
# Starting monitoring of: ~/.clawdbot/agents/main/sessions/abc.jsonl
# Starting monitoring of: ~/.clawdbot/agents/main/sessions/def.jsonl
```

When new files are detected:
```
INFO - New log files detected: ['/tmp/clawdbot/clawdbot-2026-01-26.log']
INFO - Starting monitoring of: /tmp/clawdbot/clawdbot-2026-01-26.log
```

## Best Practices

### Use Specific Patterns

✅ **Good:**
```yaml
- ~/.clawdbot/agents/main/sessions/*.jsonl
- /tmp/clawdbot/clawdbot-*.log
```

❌ **Bad (too broad):**
```yaml
- ~/.clawdbot/**/*.jsonl  # Monitors EVERYTHING recursively
- /tmp/*.log              # Monitors all logs in /tmp
```

### Balance Check Frequency

- **Default (60s)**: Good for most use cases
- **Faster (30s)**: Use if sessions start/end frequently
- **Slower (300s)**: Use if log rotation is infrequent (saves resources)

### Monitor Active Sessions Only

For Clawdbot, session files grow as the conversation continues. Archive old sessions:

```bash
# Move old sessions to archive
mkdir ~/.clawdbot/agents/main/archive
mv ~/.clawdbot/agents/main/sessions/old-*.jsonl ~/.clawdbot/agents/main/archive/
```

AgentShield will stop monitoring archived files automatically.

## Troubleshooting

### Files Not Detected

Check glob expansion manually:
```bash
ls -la ~/.clawdbot/agents/main/sessions/*.jsonl
```

Verify paths in config are correct (use absolute paths or `~/`).

### Too Many Files Monitored

Refine your patterns:
```yaml
# Instead of:
- ~/.clawdbot/**/*.jsonl

# Use:
- ~/.clawdbot/agents/main/sessions/*.jsonl
```

### Position File Issues

Reset position tracking (re-reads all files from start):
```bash
rm ~/.agentshield/.positions.json
```

## Examples

### Complete Config for Clawdbot + Claude Code

```yaml
log_level: INFO

log_paths:
  # Clawdbot - all active sessions
  - ~/.clawdbot/agents/main/sessions/*.jsonl

  # Claude Code - this project only
  - ~/.claude/projects/-Users-markbriers-Documents-Work-benchmark-ai-agentshield/*.jsonl

```

### Daily Rotation Only

```yaml
log_paths:
  # Monitor today's log (and auto-detect tomorrow's)
  - /var/log/agent/agent-*.log
```

### Session Monitoring Across Multiple Agents

```yaml
log_paths:
  # All agents, all sessions
  - ~/.clawdbot/agents/*/sessions/*.jsonl
```
