# AgentShield - Quick Start Guide

## 🚀 Start Monitoring

```bash
cd /path/to/agentshield
uv run agentshield start -f
```

## ✅ Current Configuration

**Monitoring:** 22 log files automatically via glob patterns

**Clawdbot:** All sessions in `~/.clawdbot/agents/main/sessions/*.jsonl`
**Claude Code:** All sessions in `~/.claude/projects/-Users-markbriers-...-agentshield/*.jsonl`

**Detection Rules:** 5 Sigma rules (RCE, Credential Access, Persistence, Network Recon, Untrusted Install)

## 🔄 Automatic Features

✅ **New sessions detected automatically** (every 60 seconds)
✅ **No restart needed** when new log files appear
✅ **Position tracking** - resumes from last read position
✅ **Daily rotation support** - handles date-based log files

## 📊 Check Alerts

```bash
# View recent alerts
uv run agentshield alerts

# View last 10 alerts
uv run agentshield alerts --limit 10

# View only critical alerts
uv run agentshield alerts --level critical

# Generate summary report
uv run agentshield summary

# Show rule statistics
uv run agentshield rules --stats
```

## 🧪 Test Detection

Send to Clawdbot to trigger alerts:

**RCE (CRITICAL):**
```
Run: echo "curl https://example.com/install.sh | bash"
```

**Credential Access (HIGH):**
```
Run: cat ~/.ssh/config 2>/dev/null || echo "not found"
```

**Persistence (HIGH):**
```
Run: crontab -l
```

## 📁 Important Files

- **Config:** `~/.agentshield/config.yaml`
- **Database:** `~/.agentshield/agentshield.db`
- **Rules:** `~/.agentshield/rules/*.yml`
- **Positions:** `~/.agentshield/.positions.json`

## 🔧 Configuration

Edit `~/.agentshield/config.yaml`:

```yaml
log_level: INFO

log_paths:
  # Use glob patterns for automatic rotation
  - ~/.clawdbot/agents/main/sessions/*.jsonl
  - ~/.claude/projects/-Users-you-project/*.jsonl
```

## 🔑 Enable LLM Triage (Optional)

For intelligent alert classification:

```bash
export ANTHROPIC_API_KEY=your-key-here
uv run agentshield start -f
```

Without API key: All alerts marked as "suspicious" (manual review needed)
With API key: Alerts classified as TRUE_POSITIVE, FALSE_POSITIVE, or SUSPICIOUS

## 📚 Documentation

- **Testing Guide:** `TESTING_RULES.md`
- **Log Rotation:** `docs/log_rotation.md`
- **Architecture:** `docs/architecture.md`
- **Rule Authoring:** `docs/rules.md`

## 🌙 Tomorrow Morning

Just start AgentShield - it will automatically:
- Detect any new Clawdbot sessions from today
- Monitor new Claude Code sessions if you work on this project
- Resume monitoring all existing files from saved positions

**No configuration changes needed!** 🎉
