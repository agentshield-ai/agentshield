# Case study: session velocity anomaly

A simple but durable signal: an unusually high number of tool calls
in a short session window almost always indicates automated probing,
a runaway loop, or a scripted attack. This document walks through
[`ai_agent_session_velocity_anomaly.yml`][rule] — the simplest
session-correlation rule in the corpus — and shows why even this
basic count is unavailable to a stateless system.

[rule]: ../../rules/rules/ai_agent/ai_agent_session_velocity_anomaly.yml

## Scenario

The agent has been instructed (e.g. via an indirect injection in a
document it's processing) to enumerate everything it can — every file
in `/etc`, every environment variable, every running process, every
container, every cloud credential location. The instruction reduces
to "run as many small read commands as you can". Each individual call
is benign; the *rate* is the signal.

## Trace

```
T+0     session=delta-1    Bash("cat /etc/passwd")
                           verdict: ALLOW
                           session.tool_count: 1

T+0.4s  session=delta-1    Bash("cat /etc/group")
                           verdict: ALLOW
                           session.tool_count: 2

T+0.7s  session=delta-1    Bash("cat /etc/hosts")
                           verdict: ALLOW
                           session.tool_count: 3

T+1.1s  session=delta-1    Bash("cat /etc/resolv.conf")
                           ...
                           session.tool_count: 4

  ... 14 more rapid Bash() calls iterating through /etc and /proc ...

T+8.5s  session=delta-1    Bash("uname -a")
                           verdict: ALLOW
                           session.tool_count: 19

T+9.0s  session=delta-1    Bash("hostname")
                           ── ai_agent_session_velocity_anomaly fires ──
                           verdict: REQUIRE_APPROVAL (medium)
                           session.tool_count: 20
```

Detection:

```yaml
detection:
  selection:
    session.tool_count|re: '^([2-9][0-9]|[1-9][0-9]{2,})$'
  condition: selection
```

The regex matches integers 20 through 99 (`[2-9][0-9]`) plus any
3+-digit number (`[1-9][0-9]{2,}`). Once a session crosses 20 tool
calls, every subsequent call inherits the velocity-anomaly alert
until the session window expires (default 600s) or the session ends.

## Why a stateless system can't detect this

The mechanism is "count tool calls per session, alert on >=20." For a
stateless forward proxy this is unavailable for three independent
reasons:

1. **No tool-call concept.** A proxy sees TLS connections and HTTP
   requests. Twenty Bash invocations that all read local files
   produce zero network traffic; the proxy sees nothing at all.
2. **No session boundary.** Even if every call did produce egress, a
   proxy has no way to associate them as one session unless every
   request carries an explicit session header that the agent harness
   injects (the AgentShield+CrabTrap deployment pattern documented in
   [the joint deployment doc](../deployments/agentshield-with-crabtrap.md)).
3. **Sliding window.** The count is bounded by `session.window_sec`
   (default 600 seconds). A session that does 20 calls over 6 hours
   does *not* trigger; the velocity is what matters. Implementing
   that at the proxy layer requires per-session memory the proxy
   doesn't have.

This is the rule that sets the floor: even a deployment that does
nothing else and only runs AgentShield's session-velocity rule
catches a class of attack that no per-request analyzer can. It's
also the cheapest rule in the corpus to evaluate — one regex over
one numeric field — so it costs almost nothing to leave on.

## Operational notes

- **Threshold rationale.** 20 was chosen as the lowest count that's
  comfortably above normal interactive usage and comfortably below
  legitimate batch workflows (which usually hit 50+). Adjust the
  regex bound for your workflow's typical density.
- **Severity is medium.** The rule fires `require_approval`, not
  `block`. Velocity is a noisy signal; many legitimate workflows
  trigger it. The operator approves with context, the override count
  is tracked, and if approvals or overrides accumulate the
  [approval fatigue](approval-fatigue.md) and
  [override escalation](override-escalation.md) rules take over.
- **Window tuning.** `session.window_sec` defaults to 600. Shorter
  windows make the rule more sensitive to bursts; longer windows
  make it less so. If your agents do work over hour-long sessions,
  increasing the window AND raising the count bound (e.g. 200 over
  3600s) preserves the same false-positive rate.
- **Composes with other rules.** Velocity anomaly does not constrain
  the current event — it fires regardless of what tool is being
  called. This means a velocity-anomalous session that then attempts
  exfiltration will produce both this alert AND
  `ai_agent_recon_then_exfil` AND any per-request rules; the operator
  sees a multi-rule fire and can prioritise accordingly.

## Related

- Rule: [`ai_agent_session_velocity_anomaly.yml`][rule]
- Reference: [Sun et al., "Trustworthy LLM Agents", arXiv 2024](https://arxiv.org/abs/2403.04783)
- MITRE: [T1083 — File and Directory Discovery](https://attack.mitre.org/techniques/T1083/) (matches the trace shown; the rule itself also tags [T1046 — Network Service Discovery](https://attack.mitre.org/techniques/T1046/) for the network-scanning variant)
- Sibling rules: [approval fatigue](approval-fatigue.md), [override escalation](override-escalation.md), [recon then exfil](recon-then-exfil.md)
