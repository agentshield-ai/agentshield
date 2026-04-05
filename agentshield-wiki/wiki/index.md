# Index

Content catalog for `agentshield-wiki`. Add every new page here. Grouped by type, then by
taxonomy category where applicable.

Last refreshed: 2026-04-05

---

## Orientation

- [overview](./overview.md) — what this wiki is
- [coverage](./coverage.md) — TTP ↔ Sigma rule matrix

## TTPs

### execution
- _(none yet — see [priority list](./coverage.md#priority-ttp-backlog))_

### discovery
- _(none yet)_

### exfiltration
- _(none yet)_

### persistence
- _(none yet)_

### impact
- _(none yet)_

## Rules
- _(none yet — mirror from `agentshield/rules/rules/ai_agent/`)_

## Vendors
- _(none yet)_

## Synthesis
- _(none yet)_

---

## Priority TTP backlog

The ten highest-priority TTP pages to create first (see [coverage](./coverage.md) for details):

1. `ttp.indirect-prompt-injection` — execution
2. `ttp.direct-prompt-injection` — execution
3. `ttp.tool-call-hijacking` — execution
4. `ttp.http-exfiltration-via-tools` — exfiltration
5. `ttp.dns-oob-exfiltration` — exfiltration
6. `ttp.agent-config-file-poisoning` — persistence
7. `ttp.malicious-mcp-server-install` — persistence
8. `ttp.recon-then-exfil-chain` — discovery (→ exfiltration)
9. `ttp.credential-and-secret-discovery` — discovery
10. `ttp.destructive-tool-invocation` — impact
