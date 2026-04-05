# Coverage Matrix

Maps known AI agent TTPs to Sigma rule coverage in
[`agentshield/rules/rules/ai_agent/`](https://github.com/agentshield-ai/agentshield/tree/main/rules/rules/ai_agent).

- **Coverage**: `full` = all common variants matched; `partial` = dominant variant matched,
  known gaps; `none` = no rule exists.
- **Rule IDs** mirror Sigma rule filenames (without `.yml`).
- Gaps are explicit — `coverage: none` rows are **the backlog for rule authors**.

Last refreshed: 2026-04-05

---

## execution

| TTP | Severity | Coverage | Rules | Notes |
|---|---|---|---|---|
| [indirect-prompt-injection](./ttp/indirect-prompt-injection.md) | high | none | — | Injection via fetched URL / file content — highest priority gap |
| [direct-prompt-injection](./ttp/direct-prompt-injection.md) | medium | none | — | User-supplied jailbreak / role override |
| [tool-call-hijacking](./ttp/tool-call-hijacking.md) | high | none | — | Confused-deputy / argument smuggling between tools |

## discovery

| TTP | Severity | Coverage | Rules | Notes |
|---|---|---|---|---|
| [credential-and-secret-discovery](./ttp/credential-and-secret-discovery.md) | high | none | — | `~/.ssh`, `~/.aws`, env, keychain, `.env` files |
| [recon-then-exfil-chain](./ttp/recon-then-exfil-chain.md) | high | partial | `ai_agent_recon_then_exfil` | Session-window correlation; see rule notes for bypasses |

## exfiltration

| TTP | Severity | Coverage | Rules | Notes |
|---|---|---|---|---|
| [http-exfiltration-via-tools](./ttp/http-exfiltration-via-tools.md) | high | none | — | `curl`/`wget`/`fetch` to attacker host, webhook POSTs |
| [dns-oob-exfiltration](./ttp/dns-oob-exfiltration.md) | high | none | — | DNS TXT/A queries to attacker-controlled zone |

## persistence

| TTP | Severity | Coverage | Rules | Notes |
|---|---|---|---|---|
| [agent-config-file-poisoning](./ttp/agent-config-file-poisoning.md) | critical | none | — | Writes to `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, `.clinerules` |
| [malicious-mcp-server-install](./ttp/malicious-mcp-server-install.md) | critical | none | — | `claude mcp add`, edits to MCP config JSON |

## impact

| TTP | Severity | Coverage | Rules | Notes |
|---|---|---|---|---|
| [destructive-tool-invocation](./ttp/destructive-tool-invocation.md) | critical | none | — | `rm -rf`, `git push --force`, `DROP TABLE`, pkg unpublish |

---

## Proposed rules

Rules that would close specific gaps above. These are **not yet implemented** in `agentshield`.

| Proposed ID | Closes | Sketch |
|---|---|---|
| `proposed.ai_agent_prompt_injection_fetched_content` | indirect-prompt-injection | Flag when tool output from `fetch`/`read_web` contains jailbreak phrasings, then within session window correlate with attacker-aligned tool calls |
| `proposed.ai_agent_http_exfil_to_untrusted_host` | http-exfiltration-via-tools | `curl`/`wget` with POST body or uploaded file to host outside allowlist |
| `proposed.ai_agent_dns_oob_exfiltration` | dns-oob-exfiltration | High-entropy subdomain lookups, repeated TXT queries to same zone |
| `proposed.ai_agent_config_file_poisoning` | agent-config-file-poisoning | Writes to well-known agent config filenames with imperative/instructional content |
| `proposed.ai_agent_mcp_server_install` | malicious-mcp-server-install | `claude mcp add` invocations, edits to `mcp.json` / `claude_desktop_config.json` |
| `proposed.ai_agent_secret_file_read` | credential-and-secret-discovery | Reads of `~/.ssh/*`, `~/.aws/credentials`, `.env*`, `*.pem`, `id_rsa*` |
| `proposed.ai_agent_destructive_irrecoverable_action` | destructive-tool-invocation | `rm -rf /`, `--force` pushes, `DROP`/`TRUNCATE`, `npm unpublish` |
| `proposed.ai_agent_tool_argument_smuggling` | tool-call-hijacking | Args containing instruction-like text, nested tool-call syntax, or markdown that resembles system prompts |
| `proposed.ai_agent_direct_jailbreak_pattern` | direct-prompt-injection | Known jailbreak phrasings in initial user message — low confidence, intended for audit mode |

---

## How to read / update this page

- Every `wiki/ttp/*.md` page **must** appear in the matrix above under its `category:`.
- When a new Sigma rule ships in `agentshield`, move the corresponding row from `coverage: none`
  to `partial` or `full` and link the rule from its `wiki/rules/<rule-id>.md` page.
- When removing or renaming a proposed rule, also update any TTP page that references it.
