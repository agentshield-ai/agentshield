# SkillSpector Pattern Diff & Rule Gap Analysis

Comparison of [NVIDIA/SkillSpector](https://github.com/NVIDIA/SkillSpector)
detection patterns (64 patterns / 16 categories, Apache-2.0) against
AgentShield's Sigma rule corpus, with new rules drafted to close the gaps.

## Architectural caveat

SkillSpector is a **pre-install static scanner** of skill *artifacts*
(SKILL.md + bundled scripts/configs). AgentShield evaluates **runtime
events** (tool calls, file writes, inputs/outputs). Many SkillSpector
patterns are therefore not 1:1 portable — but most map cleanly onto the
runtime signal that occurs when an agent **writes** a skill/instruction/config
file (`event_type: file_write`, matched on `content`/`file_path`), exactly as
our existing `ai_agent_rules_file_backdoor` and `ai_agent_config_auto_approve`
rules already do. That is the translation used below.

## Coverage matrix

| SkillSpector category | Patterns | AgentShield coverage | Action |
|---|---|---|---|
| Prompt Injection | 5 | `prompt_injection_direct/indirect/exfil` | Covered |
| Data Exfiltration | 4 | `data_exfiltration`, `dns_tunneling`, `lotl_exfiltration`, `steganographic_exfil`, `rag_image_exfiltration` | Covered |
| Privilege Escalation | 3 | `privilege_escalation`, `cloud_iam_escalation`, `container_escape` | Covered |
| Memory Poisoning | n | `memory_poisoning`, `context_poisoning` | Covered |
| Supply Chain | 6 | `untrusted_skill_install` (install-time only) | Partial — CVE/dependency + in-skill obfuscation still gap (defer to `feat/supply-chain-checks`) |
| MCP (least-priv + tool poisoning) | 8 | `mcp_tool_poisoning`, `mcp_rug_pull`, `mcp_config_manipulation`, `mcp_command_injection`, `shadow_mcp_credential_harvest` | Covered |
| Dangerous Code (AST) | 8 | `encoded_payload`, `shell_eval_obfuscation`, `python_download_exec` | Mostly covered |
| Tool Misuse — TM1/TM2 | — | `rce_injection`, `reverse_shell`, `scripting_tool_substitution` | Covered |
| **Excessive Agency (EA1/EA2/EA4)** | 4 | only `config_auto_approve` (structured config) | **GAP → new rule** |
| **System Prompt Leakage (P6/P7/P8)** | 3 | indirect only | **GAP → new rule** |
| **Output Handling — OH1 (unvalidated output → exec)** | — | none | **GAP → new rule** |
| **Tool Misuse — TM3 (insecure defaults)** | — | none | **GAP → new rule** |
| **Rogue Agent — RA1 (self-modification)** | — | `rules_file_backdoor` (hidden unicode only) | **GAP → new rule** |

## New rules drafted

1. `ai_agent_excessive_agency_grant.yml` — EA1/EA2/EA4. Skill/instruction
   files that grant unrestricted tool access, autonomous execution without
   approval, or remove resource/iteration limits. Complements
   `config_auto_approve` (which only matches structured config keys) by
   catching natural-language agency directives in instruction files.
2. `ai_agent_system_prompt_extraction.yml` — P6/P7/P8. Requests to reveal,
   encode, or exfiltrate the agent's own system prompt/instructions.
3. `ai_agent_unvalidated_output_execution.yml` — OH1. Model/LLM output fed
   directly into a code-execution sink (`exec`/`eval`/`subprocess`/`os.system`/
   `innerHTML`) without validation.
4. `ai_agent_insecure_defaults.yml` — TM3. Code/config that disables TLS/cert
   verification, auth, or sets wildcard CORS — weakening the agent's own
   security posture.
5. `ai_agent_skill_self_modification.yml` — RA1. An agent rewriting its own
   skill/instruction definition or disabling its own safety checks.

## Not ported (intentionally)

- **Risk scoring (0–100)** — AgentShield's block/require_approval/allow/log +
  Sigma severity + LLM triage is richer.
- **Pipeline/CLI/provider plumbing** — duplicates existing Go infrastructure.
- **OSV.dev CVE + SARIF output** — valuable but belong with the planned
  `feat/supply-chain-checks` work and a future `agentshield-scan` entrypoint,
  not the rule corpus.

## Provenance

Patterns reviewed from SkillSpector `src/skillspector/nodes/analyzers/`:
`static_patterns_excessive_agency.py`, `static_patterns_rogue_agent.py`,
`static_patterns_system_prompt_leakage.py`, `static_patterns_output_handling.py`,
`static_patterns_tool_misuse.py`. Both projects are Apache-2.0; rules below are
original AgentShield Sigma authored from the pattern taxonomy (no code copied).
