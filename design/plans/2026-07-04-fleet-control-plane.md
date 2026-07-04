# Fleet Control Plane Implementation Plan

**Goal:** Give a security/platform team one place to see every AgentShield-protected agent in their fleet: a live verdict stream, per-session behavioural timelines, an alert triage queue, and cross-session attack correlation — with org/team scoping, SSO, and RBAC. This is the primary monetization surface: the edge engine stays free and open; the control plane is the paid product.

**Architecture:** A new Go service (`agentshield-cp`) that receives batched telemetry from edge engines over an authenticated HTTP ingest API, persists it in Postgres, and serves a REST API + web dashboard. Edge engines gain a small, fail-open **forwarder** that ships verdicts, alerts, session snapshots, and overrides asynchronously — the evaluate hot path is never blocked and never depends on the control plane being reachable.

**Status:** Proposed (not started)

---

## First-principles constraints

These are the requirements we keep; everything else gets questioned:

1. **The edge engine must not get worse.** Evaluation latency, fail-open behaviour, and offline operation are the product's core promise. The forwarder is async, buffered, and bounded. If the control plane is down for a week, the engine doesn't notice.
2. **The control plane must be self-hostable.** The buyer is a security team; many will refuse SaaS for agent telemetry (it contains commands, file paths, prompts). Same binary runs as our hosted service and as the customer's on-prem deployment. This also keeps us honest: no hosted-only magic.
3. **Agent-security-specific, not a SIEM.** We already export OTel to Splunk/Datadog/Elastic for generic log search. The control plane's views are things a SIEM cannot render: session manipulation timelines, recon→exfil chains, approval-fatigue trends, cross-session correlation. If a screen could be a Grafana dashboard, it doesn't belong here.
4. **Delete the parts:** no Kafka, no ClickHouse, no microservices at v1. One Go binary, one Postgres. A single Postgres comfortably handles tens of millions of rows/day with the write pattern we have (append-only, batch inserts). Revisit only when a real customer exceeds it.

## Non-goals (v1)

- **Central policy push** — separate plan (approval routing + policy management). We reserve schema/API space for it but build nothing.
- **Managed rules feed** — separate plan. The control plane *displays* which rules version each engine runs (already available via lifecycle events), nothing more.
- **Billing/metering** — needed for the hosted tier eventually; not needed to validate the product.
- **Raw event firehose storage** — we ingest verdicts/alerts/session data, not every allowed tool call by default (engines can opt in via `forward_all_events` for high-trust environments; the schema supports it).

---

## What exists today (and what we reuse)

| Existing piece | Role in this plan |
|---|---|
| `internal/store/store.go` — SQLite `alerts` + `feedback` tables | Stays as-is for edge-local persistence. The forwarder taps the same code paths that write these rows. |
| `internal/session/registry.go` — per-session windows, derived fields, cross-session correlation | Source of `session_snapshot` payloads; the dashboard's timeline view is largely a rendering of this data. |
| `internal/telemetry/` — OTel traces/metrics | Unchanged. OTel remains the SIEM integration; the forwarder is a separate, richer, first-class channel. |
| `POST /api/v1/override`, `/audit`, `/lifecycle` handlers in `internal/server/server.go` | Each of these becomes a forwarder emit point in addition to local handling. |
| `internal/auth/` — constant-time token comparison | Reused for ingest API key verification (hashed at rest, constant-time compare). |
| `internal/config/config.go` | Gains a `control_plane:` section. |
| `cmd/agentshield-replay` | Doubles as a load generator and demo-data seeder: replay HF traces through an engine with forwarding enabled to populate a control plane instance. |

---

## System design

```
┌── customer laptop/CI/server ──────────────┐
│  Agent (Claude Code / OpenClaw / MCP GW)  │
│    │ hook                                 │
│  AgentShield engine (existing)            │
│    ├── evaluate → verdict  (unchanged)    │
│    ├── SQLite   (unchanged)               │
│    └── forwarder (NEW, internal/forward)  │
│          bounded queue, batch, retry      │
└──────────┬────────────────────────────────┘
           │ HTTPS  POST /ingest/v1/batch   (per-engine API key)
           ▼
┌── control plane (NEW, cmd/agentshield-cp) ─────────────┐
│  ingest API ──→ Postgres (orgs, engines, sessions,     │
│                           alerts, verdicts, overrides) │
│  query API  ──→ REST /cp/v1/*  (RBAC-scoped)           │
│  dashboard  ──→ static SPA served from same binary     │
│  auth       ──→ OIDC (SSO) + API tokens                │
└────────────────────────────────────────────────────────┘
```

### Data model (Postgres)

```
orgs            (id, name, created_at)
teams           (id, org_id, name)
users           (id, org_id, email, oidc_subject, created_at)
memberships     (user_id, team_id, role)          -- role: admin | analyst | viewer
engines         (id, org_id, team_id, name, enroll_key_hash, last_seen_at,
                 engine_version, rules_version, mode, platform, revoked_at)
sessions        (id, org_id, engine_id, external_session_id, started_at,
                 last_event_at, tool_count, unique_tool_count, alert_count,
                 approval_count, override_count, risk_score)
verdicts        (id, org_id, engine_id, session_id, event_id, tool_name,
                 event_type, action, mode, matched_rule_ids[], severity,
                 cached, latency_ms, created_at)      -- partitioned by month
alerts          (id, org_id, engine_id, session_id, event_id, rule_id,
                 rule_name, severity, tool, args_redacted, action_taken,
                 triage_status, triage_verdict, assignee_user_id, created_at)
overrides       (id, org_id, alert_id, session_id, engine_id, actor,
                 original_action, created_at)
ingest_dedup    (org_id, event_id, seen_at)           -- idempotency, TTL 24h
```

Notes:
- `event_id` (already generated per evaluation) is the idempotency key — ingest is at-least-once, storage is exactly-once.
- `verdicts` is the high-volume table: monthly partitions, retention policy per org (default 90 days, mirroring the edge `retention_days` convention).
- `alerts.triage_status` (`open | acknowledged | resolved_fp | resolved_tp | escalated`) powers the triage queue and — critically — feeds the existing `feedback` loop: resolving an alert as FP in the dashboard posts feedback back through the same semantics as the edge `POST /api/v1/feedback`.
- Cross-session correlation is computed **again** control-plane-side across the whole org (the edge registry only sees its own process). Same algorithm as `internal/session`, wider blast-radius visibility. This is the feature single-node users cannot get and the screenshot that sells the product.

### Ingest contract

`POST /ingest/v1/batch` — `Authorization: Bearer <engine key>`

```json
{
  "engine": {"id": "eng_...", "version": "1.4.0", "rules_version": "sha...", "mode": "enforce"},
  "sent_at": "2026-07-04T12:00:00Z",
  "items": [
    {"kind": "verdict",  "event_id": "...", "session_id": "...", "tool": "Bash",
     "event_type": "tool_call", "action": "block", "matched_rules": ["ai_agent_rce_..."],
     "severity": "critical", "cached": false, "latency_ms": 3, "ts": "..."},
    {"kind": "alert",    "event_id": "...", "rule_id": "...", "rule_name": "...",
     "severity": "high", "tool": "Bash", "args_redacted": "curl -d @~/.ssh/... ", "ts": "..."},
    {"kind": "session_snapshot", "session_id": "...", "recent_tools": ["Read","Grep","Bash"],
     "tool_count": 41, "alert_count": 2, "approval_count": 1, "override_count": 0, "ts": "..."},
    {"kind": "override", "event_id": "...", "session_id": "...", "actor": "user",
     "original_action": "block", "ts": "..."},
    {"kind": "heartbeat", "ts": "..."}
  ]
}
```

Response: `{"accepted": N, "duplicates": M}`. Server enforces max body size (1 MiB), max 500 items/batch, UTF-8 validation and control-character rejection on all string fields (same conventions as the edge server).

**Redaction at the edge, not the server.** `args_redacted` is produced by the forwarder before anything leaves the machine: secrets-pattern scrubbing (API keys, bearer tokens, `AWS_SECRET...`) and a configurable max arg length. The control plane never receives what it shouldn't store. This is a selling point for security buyers and must be in the ingest contract from day one — retrofitting redaction breaks trust.

### Query API (dashboard backend)

All under `/cp/v1`, session-cookie or API-token auth, org-scoped by RBAC:

- `GET /fleet` — engines with liveness, mode, rules version, 24h alert counts
- `GET /verdicts?since=&engine=&action=&severity=` — cursor-paginated stream (and `GET /verdicts/live` — SSE for the live view; SSE not WebSockets — simpler, proxy-friendly, one-directional is all we need)
- `GET /sessions/:id/timeline` — ordered verdicts + alerts + overrides for the session
- `GET /alerts?status=open&severity=...` / `PATCH /alerts/:id` — triage queue
- `GET /correlations` — active cross-session correlation clusters
- `POST /engines` / `POST /engines/:id/revoke` — enrollment (returns the key once), revocation
- `GET /orgs /teams /users` + membership CRUD — admin only

### Dashboard (SPA)

TypeScript + React + Vite, embedded into the Go binary with `embed.FS` — one artifact to deploy, no separate frontend service. (TypeScript is already established in this repo via the plugins; no new language.) Five screens, in build order:

1. **Fleet overview** — engine grid: online/offline, mode, rules version drift, alert sparklines.
2. **Live verdict stream** — SSE-fed table with action/severity/engine filters. This is the "five minutes after install" wow screen.
3. **Session timeline** — the differentiated view: tool-call sequence with alerts inline, recon→exfil chain highlighting, approval/override markers.
4. **Alert triage queue** — assign, acknowledge, resolve as FP/TP (writes feedback), bulk actions.
5. **Correlation view** — cross-session clusters ("3 sessions on 3 engines hit the same injection pattern in 5 minutes").

Auth screens (login via OIDC, org/team admin) are boring and last.

---

## Implementation phases

### Phase 0 — Edge forwarder (`internal/forward/`) and config

The contract-first phase: everything downstream depends on this, and it ships value even before the control plane exists (customers can point it at any HTTP collector).

- **Create `internal/forward/forwarder.go`** — bounded in-memory queue (default 10k items, drop-oldest with a dropped-items counter exposed on `/health` and as an OTel metric), background flusher (batch up to 500 items or 5s interval, whichever first), `retryablehttp` with exponential backoff, hard timeout so shutdown is never blocked (flush-with-deadline on SIGTERM).
- **Create `internal/forward/redact.go`** — secrets scrubbing + arg truncation before enqueue. Table-driven tests with known key formats (AWS, GitHub, Anthropic, OpenAI, generic `Bearer`).
- **Config** (`internal/config/config.go`):
  ```yaml
  control_plane:
    enabled: false
    endpoint: https://cp.example.com
    api_key: ${AGENTSHIELD_CP_KEY}
    engine_name: "ci-runner-42"        # display name; ID assigned at enrollment
    buffer_size: 10000
    flush_interval_sec: 5
    forward_all_events: false          # verdicts for allowed events too
  ```
  Env vars: `AGENTSHIELD_CP_ENDPOINT`, `AGENTSHIELD_CP_KEY`.
- **Emit points** — one call each where the data already exists: after verdict decision in `internal/evaluate`, after alert persistence in the store path, in `handleOverride`, `handleLifecycleEvent`, and a session-snapshot emit on window update in `internal/session` (throttled: at most one snapshot per session per flush interval).
- **Tests:** forwarder unit tests (bounded buffer, batch cutting, retry, shutdown deadline, redaction), plus a `security_test.go` per repo convention (oversized fields, control characters, key never logged).
- **Invariant test:** evaluate-path benchmark asserting forwarding-enabled adds zero blocking work to the hot path (enqueue is a non-blocking channel send).

### Phase 1 — Control plane service: ingest + storage + query API

- **Create `cmd/agentshield-cp/`** and `internal/cp/{server,store,ingest,authz}/`. Same stack as the edge: chi, `log/slog`, graceful shutdown via the existing `daemon` patterns.
- **Postgres via `pgx`** (first new heavyweight dependency — justified: this service is multi-writer and concurrent in a way SQLite isn't). Migrations with `golang-migrate`, schema as above. `docker/docker-compose.cp.yaml` for local dev (cp + postgres).
- **Ingest handler:** key lookup (SHA-256 of presented key → engines row, constant-time compare), idempotency via `ingest_dedup`, batch insert per kind, body/field limits.
- **Query API:** the read endpoints above, single-org hardcoded this phase (org 1), API-token auth only. RBAC middleware lands in Phase 3 but the handler signatures take a scope from day one so nothing gets rewritten.
- **Org-wide correlation job:** background goroutine recomputing cross-session clusters every 30s over the last 5 minutes (port of `internal/session` correlation logic against Postgres).
- **Integration test:** docker-compose — edge engine (forwarding on) + cp + postgres; drive the edge with `agentshield-replay --http`; assert verdicts/alerts/sessions appear via the query API. This test is also the demo-seeding script.

### Phase 2 — Dashboard

- **Create `cp/web/`** (Vite + React + TS), built in CI, embedded via `embed.FS`. Screens in the order listed above; fleet overview + live stream are the milestone for "demoable".
- SSE endpoint for the live stream (`/cp/v1/verdicts/live`).
- Alert triage actions wired to `PATCH /alerts/:id`; FP/TP resolution recorded in a `cp_feedback` table shaped like the edge `feedback` table so the (future) rules-feed flywheel consumes one format.

### Phase 3 — Multi-org, SSO, RBAC, enrollment

- OIDC login (`coreos/go-oidc`) — works with Okta/Entra/Google out of the box; session cookies, CSRF protection.
- RBAC middleware: `admin` (manage org/teams/engines), `analyst` (triage, view all), `viewer` (read-only). Enforced in the authz package, not per-handler.
- Enrollment flow: admin creates engine → gets one-time key → drops it in edge config. `agentshield cp enroll --endpoint ... --token ...` CLI helper writes the config section.
- Engine revocation, key rotation, per-org retention setting + partition-drop job.

### Phase 4 — Hosted-tier hardening (pre-launch, not pre-validation)

Per-org ingest rate limits, usage counters (events/day per org — the future billing meter), audit log of dashboard actions (who resolved which alert — itself a compliance feature), backup/restore documentation for self-hosters.

---

## Sequencing and validation gates

Ship order is deliberately demo-first:

1. **Phase 0 + Phase 1** → end of this: `replay → engine → cp → curl the query API` works. Validation gate: does the session timeline data actually tell a story on real HF traces? If not, fix the data model before building UI on it.
2. **Phase 2 (screens 1–3)** → the sellable demo: install engine, get prompt-injected, watch it in the dashboard. This is the artifact to put in front of prospective users **before** building Phase 3.
3. **Phase 3** only after ≥1 design partner says "we'd deploy this if it had SSO" — SSO/RBAC is table stakes to *close*, not to *validate*.

## Risks

- **Postgres scaling ceiling** — mitigated by partitioning + retention; escape hatch is swapping the verdicts table to ClickHouse behind the same store interface. Don't pre-build it.
- **Telemetry sensitivity** — mitigated by edge-side redaction, self-hosting, and not forwarding allowed-event args by default. Document the data flow explicitly (`docs/control-plane.md`) — security buyers will ask.
- **Two products, one repo** — cp code stays under `internal/cp/` + `cmd/agentshield-cp/` with no imports from edge packages except shared `models`; if the split hurts later, extraction is mechanical.
- **Scope creep toward SIEM** — the non-goals section is the contract; any "add log search" request routes to the OTel export instead.

## Success criteria

- Evaluate-path P99 unchanged with forwarding enabled (benchmarked in CI alongside `bench.yml`).
- Fresh laptop → engine installed → blocked attack visible in hosted dashboard in **under 5 minutes**, scripted end-to-end.
- A 100-engine simulated fleet (replay-driven) sustained on one cp instance + one Postgres with ingest latency P99 < 250ms.
- Cross-session correlation surfaces a seeded 3-session coordinated attack within 60s of the last event.
