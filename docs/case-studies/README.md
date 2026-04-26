# Behavioural correlation case studies

These four case studies cover the AgentShield rules whose detection
mechanism depends on **per-session memory** — a property of the
tool-call layer that the network layer (forward proxies, IDS) does
not have.

The case studies share a common shape:

1. **Scenario.** What the attacker is doing and why each individual
   request looks ordinary.
2. **Trace.** A timeline of evaluation events with the relevant
   `session.*` fields rising on each step.
3. **Detection mechanism.** The rule excerpt that fires, with the
   specific regex or selector logic.
4. **Why a stateless system can't detect this.** The structural
   argument: which pieces of state the rule depends on, why they
   cannot exist at the network layer.
5. **Operational notes.** Threshold tuning, false-positive guidance,
   how the rule composes with the others.

Traces are illustrative — composed from realistic tool-call shapes
seen in agent traces, not single captured sessions. Detection
mechanisms, fields, and verdicts are exactly as the rules fire today;
integration verification lives in the bench testcase suite and the
Go integration tests.

## The four rules

| Rule | Severity | Signal | Case study |
|---|---|---|---|
| `ai_agent_recon_then_exfil` | high | Recon tool in session history + exfil tool in current event | [recon-then-exfil.md](recon-then-exfil.md) |
| `ai_agent_session_velocity_anomaly` | medium | `session.tool_count >= 20` | [session-velocity-anomaly.md](session-velocity-anomaly.md) |
| `ai_agent_approval_fatigue` | medium | `session.approval_count >= 5` | [approval-fatigue.md](approval-fatigue.md) |
| `ai_agent_override_escalation` | high | `session.override_count >= 3` | [override-escalation.md](override-escalation.md) |

## What unifies them

Every detection above reduces to the same structural argument: the
rule needs to know **what the same agent did just before**, where
"just before" means within the session's TTL window. This memory
lives in [`internal/session/registry.go`](../../internal/session/registry.go)
and is exposed to the rule engine as a fixed set of derived fields:

- `session.tool_count` — total events in the session window
- `session.unique_tool_count` — distinct tool names
- `session.recent_tools` — comma-joined list, oldest first
- `session.alert_count` — total alerts produced by prior events
- `session.approval_count` — events that produced `require_approval`
- `session.override_count` — operator overrides (via `POST /api/v1/override`)

Plus cross-session aggregates for systemic-attack detection
(`session.cross_session_alert_count`,
`session.cross_session_count`,
`session.cross_session_tool_overlap`).

A network proxy can in principle observe individual requests and
allow/block them based on static rules + an LLM judge. None of those
mechanisms can read these fields, because they depend on:

- A persistent session identity that survives across short TLS
  connections;
- Knowledge of *AgentShield's own verdicts* (approval, override) on
  prior calls;
- Memory bounded by an evaluation policy, not a network connection
  lifetime.

The case studies below each explain which fields they read and why
the equivalent signal cannot be reconstructed downstream.

## Pairing with a forward proxy

These rules complement, not replace, a network-layer enforcement
plane. A deployment that runs AgentShield + a forward proxy (e.g.
[CrabTrap](https://github.com/brexhq/CrabTrap), via the
[`agentshield-proxy-adapter`](../deployments/agentshield-with-crabtrap.md))
gets:

- Tool-call-layer detection (these four rules and the rest of the
  corpus) on calls the agent harness intercepts directly.
- Network-layer detection on connections that originate from
  subprocesses the agent harness can't intercept (`npm install`, a
  downloaded binary, etc.).
- **Layer-spanning correlation** — proxy observations and tool-call
  events sharing one `session_id`, so e.g.
  [`ai_agent_recon_then_proxy_egress`](../../rules/rules/ai_agent/ai_agent_recon_then_proxy_egress.yml)
  can fire on `(prior tool-call activity) + (proxy-observed
  internal-network connection)`.

The recon → exfil case study has a pointer to the proxy-observed
variant; the rest stand on their own at the tool-call layer.

## Reading order

If you're new to AgentShield's session model, read in this order:

1. [Session velocity anomaly](session-velocity-anomaly.md) — the
   simplest correlation; one numeric field, one regex.
2. [Recon then exfil](recon-then-exfil.md) — two-selector
   correlation across the session window.
3. [Approval fatigue](approval-fatigue.md) — verdict-pattern
   correlation; introduces the human-in-the-loop trap framing.
4. [Override escalation](override-escalation.md) — second-order
   verdict correlation; the strongest example of a detection that
   only the verdict-aware layer can host.

## Related

- [Architecture](../architecture.md) — pipeline overview
- [Rules](../rules.md) — Sigma rule authoring + the `url.*` and
  `session.*` field reference
- [Joint deployment with CrabTrap](../deployments/agentshield-with-crabtrap.md)
  — proxy + tool-call layer together
- AI Agent Traps taxonomy — Franklin et al.,
  [SSRN 2026](https://papers.ssrn.com/sol3/papers.cfm?abstract_id=6372438)
