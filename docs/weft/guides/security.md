# Security

AgentShield checks every tool call your agents make against its rules and
answers **allow**, **log**, **needs approval** or **block**. Weft stores those
verdicts as **detections** on the exact tool call, so you can see what an agent
tried to do, why, and what AgentShield decided.

## Find why a session was blocked

From the Sessions page, either click the **Blocked** badge on a session, or
open the **Blocked or needing approval** card and then the session. Weft
opens the timeline at the blocked tool call, with the rule that fired and the
reasoning that led the agent there.

## Read the Security page

- **Figures:** verdicts in the time range, by action, and how many a person
  overrode.
- **Over time:** verdicts per hour or day, by action or by severity.
- **Rules that fire most:** click a rule to list only its detections.
- **By agent:** each agent's verdicts out of its tool calls. Like the rate,
  the counts are AgentShield's live verdicts; replays are not counted.
- **Approval fatigue:** sessions that asked a person for approval many times.
  People start approving without reading.
- **Override escalation:** sessions where people kept overriding verdicts.
- **Detections:** the newest first. **Show step** opens the session at the
  tool call.

The filter bar narrows all of it. Filters about a session as a whole, such as
`team:` or `status:failed`, keep the detections in matching sessions. See
[Filters and saved views](filters.md).

## Replay recorded tool calls after a rule change

When the rules change, choose **Replay** in the "Rules changed?" panel. Weft
re-checks the recorded tool calls in the time range (or the sessions matching
your filters) with the current rules, without touching your agents, and
shows which calls would now be blocked, which no longer would, and which
changed. The result depends only on the recorded calls and the rules:
replaying the same sessions twice gives the same answer, however busy a live
engine is in between. Rules that look across sessions see the other sessions
of the same replay, never live traffic, and a replay never changes a live
verdict.

Replay needs the server to know the rules: start it with
`--shield-rules-dir rules/rules`, or with `--shield-url` pointing at a
running engine.

## Connect AgentShield

Add this to the engine's `config.yaml`, with your Weft address and project:

```yaml
weft:
  endpoint: http://127.0.0.1:6345
  project: my-team/agents
```

The engine then sends its verdicts to Weft in the background, without ever
slowing a verdict down. Verdicts attach to the right tool call when the
AgentShield plugin and the Weft plugin (or SDK) see the same session and
tool-call ids, which the coding-agent plugins do for you. The
[reference](../README.md#agentshield-integration) describes the OpenTelemetry
alternative and how verdicts are matched to tool calls.
