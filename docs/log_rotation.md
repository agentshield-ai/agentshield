# Log Retention and Rotation

AgentShield stores alerts and feedback in a SQLite database. This document covers data retention and log management.

## Database Storage

All alerts, triage results, and feedback are stored in the SQLite database configured via `store.sqlite_path` (default: `./agentshield.db`).

## Automatic Retention

The engine supports automatic data cleanup:

```yaml
store:
  sqlite_path: "./agentshield.db"
  retention_days: 90          # Delete alerts older than N days (default: 90)
  cleanup_interval_hours: 24  # How often to run cleanup (default: 24)
```

Set `retention_days: 0` to disable automatic cleanup.

## Manual Maintenance

For periodic database maintenance, use the SQLite CLI:

```bash
# Reclaim disk space after large deletions
sqlite3 /path/to/agentshield.db "VACUUM;"

# Check database integrity
sqlite3 /path/to/agentshield.db "PRAGMA integrity_check;"
```

## Structured Logging

The engine emits structured JSON logs via `log/slog` to stderr. Use standard log rotation tools (e.g., `logrotate`, systemd journal) to manage log output:

```bash
# Redirect to a log file with rotation
agentshield serve --config config.yaml 2>&1 | logger -t agentshield

# Or via systemd journal (see deployment.md for systemd unit)
journalctl -u agentshield -f
```
