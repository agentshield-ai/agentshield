# AgentShield Forensics Console (offline-first)

A SIEM-inspired frontend for retrospective/forensic analysis of AgentShield alerts.

## Why this design

Borrowed patterns from mature SIEM UX (Elastic/Sentinel style):
- **Fast triage queue** (sortable alert table)
- **Faceted filtering** (severity/action/tool/rule)
- **Time trend first** (timeline for surge spotting)
- **Drill-down details** (raw record viewer)
- **Analyst workflow helpers** (saved views, local notes, filtered export)

This frontend is static and can be opened from disk (`file://.../index.html`).

## Data ingestion

The app currently loads:
- JSON array (`[ {...}, {...} ]`)
- NDJSON/JSONL (one JSON object per line)
- SQLite DB directly (`.db/.sqlite/.sqlite3`) via in-browser `sql.js`

The UI now bundles `sql.js` assets locally in `forensics-ui/vendor/`, so direct DB loading works offline from disk (`file://`) with no network dependency.

## Export from SQLite

Use sqlite3 to export alerts from AgentShield DB:

```bash
sqlite3 /home/agent/.agentshield/agentshield.db \
  -cmd ".headers off" \
  -cmd ".mode json" \
  "SELECT id, rule_name, severity, tool, args, action_taken, timestamp, session_id, event_id FROM alerts ORDER BY timestamp DESC;" \
  > alerts.json
```

Then open `index.html` and load `alerts.json`.

## Fields expected

Best experience with these fields:
- `timestamp`
- `severity`
- `rule_name`
- `tool`
- `action_taken`
- `session_id`
- `event_id`
- `args` (optional)

## Future upgrades

- Optional direct `.db` loading via `sql.js` (WASM) with local bundled assets
- ATT&CK mapping panel per rule
- Session graph view (entity links + sequence)
- Case timeline exports (PDF/Markdown)
