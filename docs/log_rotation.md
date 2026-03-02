# Log Retention and Rotation

This document describes data retention policies and log management for the AgentShield engine.

## Database Storage

All alerts, triage results, and feedback are persisted in a SQLite database. The database path is configured via `store.sqlite_path` in `config.yaml` (default: `./agentshield.db`).

## Automatic Retention

The engine supports automatic data cleanup via two configuration options:

```yaml
store:
  sqlite_path: "./agentshield.db"
  retention_days: 90          # Delete alerts older than N days (default: 90)
  cleanup_interval_hours: 24  # How often to run the cleanup job (default: 24)
```

Set `retention_days: 0` to disable automatic cleanup entirely.

## Manual Maintenance

For periodic database maintenance, use the SQLite CLI:

```bash
# Reclaim disc space after large deletions
sqlite3 /path/to/agentshield.db "VACUUM;"

# Check database integrity
sqlite3 /path/to/agentshield.db "PRAGMA integrity_check;"
```

## Structured Logging

The engine emits structured JSON logs via Go's `log/slog` package to stderr. Use standard log rotation tools (e.g., `logrotate` on Linux, the systemd journal) to manage log output:

```bash
# Redirect to the system logger
agentshield serve --config config.yaml 2>&1 | logger -t agentshield

# Or monitor via the systemd journal (see deployment.md for the systemd unit file)
journalctl -u agentshield -f
```
