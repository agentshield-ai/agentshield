# Automated Threat Sweep — Design Document

## Goal

A weekly automated pipeline that researches new AI agent attack vectors, checks AgentShield's Sigma rule coverage, writes and tests new rules for uncovered threats, and delivers results via WhatsApp summary and GitHub PR.

## Architecture

Implemented as an OpenClaw skill (`/threat-sweep`) on agent-dev, triggered by a weekly cron job. The main agent orchestrates 5 sub-agents across 3 sequential phases.

```
Cron (Monday 07:00 Europe/London)
  └─ main agent receives: "/threat-sweep"
       ├─ Sub-agent 1: RESEARCH     — web_search for new attack vectors
       ├─ Sub-agent 2: COVERAGE     — map existing rules vs MITRE ATT&CK
       │
       │  (orchestrator waits, synthesises gaps)
       │
       ├─ Sub-agent 3: RULE AUTHOR  — write Sigma rules for gaps (max 3)
       ├─ Sub-agent 4: TEST ATTACK  — run malicious prompts via openclaw agent
       ├─ Sub-agent 5: TEST BENIGN  — run benign prompts, check no false positives
       │
       │  (orchestrator compiles report)
       │
       └─ DELIVER: WhatsApp summary + GitHub PR with validated rules
```

## Skill Structure

```
~/.openclaw/skills/threat-sweep/
├── SKILL.md              # Orchestrator instructions + skill metadata
├── prompts/
│   ├── research.md       # Phase 1: threat intelligence gathering
│   ├── coverage.md       # Phase 2: rule coverage analysis
│   ├── rule-author.md    # Phase 3: Sigma rule authoring
│   ├── test-malicious.md # Phase 4: attack prompt testing
│   └── test-benign.md    # Phase 5: false positive testing
└── config.yaml           # Search queries, MITRE taxonomy, delivery settings
```

## Phase Details

### Phase 1 — RESEARCH (sub-agent, parallel with Phase 2)

Uses `web_search` tool with configurable queries from `config.yaml`. Searches for AI agent security threats published in the last 7 days.

Output: JSON array of findings, each with threat name, description, MITRE ATT&CK mapping, source URL, and detection hint.

Queries (configurable):
- "AI agent attack vectors 2026"
- "LLM tool use exploitation techniques"
- "prompt injection new evasion methods"
- "AI agent data exfiltration novel"
- "MCP server security vulnerabilities"
- "agentic AI red team findings"

### Phase 2 — COVERAGE (sub-agent, parallel with Phase 1)

Reads all `.yml` files from the rules directory. Extracts title, id, status, tags from each. Maps against the MITRE ATT&CK taxonomy defined in `config.yaml`.

Output: JSON object with covered techniques (with rule counts), uncovered gaps, and rules eligible for promotion from test/experimental to stable.

### Phase 3 — RULE AUTHOR (sub-agent, after Phases 1+2)

Receives gap analysis from orchestrator. Writes up to 3 new Sigma rules to `/tmp/agentshield-new-rules/`.

Constraints:
- All required fields for `status: stable`
- UUID v5 rule IDs
- Standard Sigma operators only (contains, re, endswith, startswith, all)
- At least one MITRE ATT&CK reference per rule
- At least one documented false positive
- `product: ai_agent`, `category: agent_events`
- Also outputs malicious and benign test prompts for each rule

### Phase 4 — TEST MALICIOUS (sub-agent, after Phase 3)

For each new rule:
1. Temporarily deploy rule to engine (copy to rules dir, SIGHUP reload)
2. Run the malicious test prompt via `openclaw agent --agent main -m "..."`
3. Check `/api/v1/alerts` for new alert matching the rule
4. Report pass (rule fired) or fail (rule did not fire)

### Phase 5 — TEST BENIGN (sub-agent, parallel with Phase 4)

For each new rule:
1. Run the benign test prompt via `openclaw agent --agent main -m "..."`
2. Check `/api/v1/alerts` to confirm no new alerts
3. Report pass (no false positive) or fail (false positive detected)

### Delivery (orchestrator, after Phases 4+5)

1. Compile report: new threats found, gaps identified, rules written, test results
2. If all tests pass for a rule:
   - `git checkout -b feat/weekly-rules-YYYY-MM-DD`
   - Copy validated rules from `/tmp/agentshield-new-rules/` to rules dir
   - Run `uv run --with sigma-cli --with pySigma -- sigma check -e -i` for validation
   - Run `uv run --with pytest --with pyyaml -- pytest tests/ -v` for conformance
   - Commit, push, open PR via `gh pr create`
3. Remove temporarily deployed test rules (revert SIGHUP)
4. Send WhatsApp summary via `message` tool
5. If any tests fail: include failures in WhatsApp, exclude failing rules from PR

## Configuration (`config.yaml`)

```yaml
schedule:
  cron: "0 7 * * 1"
  timezone: "Europe/London"

research:
  queries:
    - "AI agent attack vectors 2026"
    - "LLM tool use exploitation techniques"
    - "prompt injection new evasion methods"
    - "AI agent data exfiltration novel"
    - "MCP server security vulnerabilities"
    - "agentic AI red team findings"
  lookback_days: 7
  max_findings: 10

rules:
  max_new_per_run: 3
  staging_dir: "/tmp/agentshield-new-rules"
  rules_dir: "~/.agentshield/rules/rules/ai_agent"

delivery:
  whatsapp:
    enabled: true
    channel: "whatsapp"
    peer: "default"
  github:
    enabled: true
    repo: "agentshield-ai/sigma-ai"
    branch_prefix: "feat/weekly-rules"

engine:
  endpoint: "http://127.0.0.1:8433"

taxonomy:
  tactics:
    - id: TA0001
      name: initial-access
      techniques:
        - {id: T1190, name: "Exploit Public-Facing Application"}
        - {id: T1195, name: "Supply Chain Compromise"}
    - id: TA0002
      name: execution
      techniques:
        - {id: T1059, name: "Command and Scripting Interpreter"}
        - {id: T1203, name: "Exploitation for Client Execution"}
    - id: TA0003
      name: persistence
      techniques:
        - {id: T1546, name: "Event Triggered Execution"}
        - {id: T1053, name: "Scheduled Task/Job"}
    - id: TA0004
      name: privilege-escalation
      techniques:
        - {id: T1548, name: "Abuse Elevation Control Mechanism"}
    - id: TA0005
      name: defense-evasion
      techniques:
        - {id: T1027, name: "Obfuscated Files or Information"}
        - {id: T1140, name: "Deobfuscate/Decode Files"}
    - id: TA0006
      name: credential-access
      techniques:
        - {id: T1552, name: "Unsecured Credentials"}
        - {id: T1555, name: "Credentials from Password Stores"}
    - id: TA0007
      name: discovery
      techniques:
        - {id: T1082, name: "System Information Discovery"}
        - {id: T1083, name: "File and Directory Discovery"}
    - id: TA0008
      name: lateral-movement
      techniques:
        - {id: T1021, name: "Remote Services"}
    - id: TA0009
      name: collection
      techniques:
        - {id: T1005, name: "Data from Local System"}
        - {id: T1074, name: "Data Staged"}
    - id: TA0010
      name: exfiltration
      techniques:
        - {id: T1048, name: "Exfiltration Over Alternative Protocol"}
        - {id: T1041, name: "Exfiltration Over C2 Channel"}
    - id: TA0011
      name: command-and-control
      techniques:
        - {id: T1071, name: "Application Layer Protocol"}
        - {id: T1105, name: "Ingress Tool Transfer"}
```

## Error Handling

| Failure | Mitigation |
|---------|------------|
| Web search returns nothing | Report "no novel findings", skip rule authoring, WhatsApp "no new threats this week" |
| Sub-agent times out | `runTimeoutSeconds: 300` per sub-agent. Orchestrator reports which phase failed |
| Invalid Sigma YAML | `sigma check` run before testing. Invalid rules dropped with note in report |
| Malicious test doesn't trigger | Rule excluded from PR. Flagged in WhatsApp for manual investigation |
| Benign test gets blocked | Rule excluded from PR. Flagged prominently in WhatsApp as false positive |
| GitHub PR creation fails | WhatsApp still sent. Rules left in staging dir for manual retrieval |
| Engine is down | Health check before testing phases. If down, skip testing, report in WhatsApp |

## Guardrails

- Max 3 new rules per run
- No auto-deploy — rules only land in a PR for human review
- Benign tests mandatory — no rule enters PR without passing benign test
- Staging directory cleaned at start of each run
- Sub-agent isolation — each runs in its own session
- Conformance tests must pass before PR creation

## Cron Registration

```bash
openclaw cron add \
  --name "Weekly Threat Sweep" \
  --cron "0 7 * * 1" \
  --tz "Europe/London" \
  --session isolated \
  --message "/threat-sweep" \
  --announce
```

## Manual Trigger

```bash
# From within an OpenClaw session
/threat-sweep

# Or via CLI
openclaw agent --agent main -m "/threat-sweep"
```
