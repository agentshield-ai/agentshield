# AgentShield — AI Agent Detection & Response (AADR)

Real-time security monitoring for AI agents using Sigma rules

![License](https://img.shields.io/badge/License-Apache%202.0-green)
![Status](https://img.shields.io/badge/Status-Production%20Ready-brightgreen)
![Rules](https://img.shields.io/badge/Rules-36+-blue)

## What is AgentShield?

AgentShield monitors AI agent tool calls, evaluates them against Sigma detection rules, blocks malicious activity, and provides LLM-powered triage. It's the first comprehensive security framework designed specifically for AI agents, protecting against prompt injection, tool poisoning, credential theft, data exfiltration, and other AI-specific threats.

## Architecture

```
┌─────────────────┐
│  OpenClaw       │──┐
│  agentshield-oc │  │     ┌──────────────────┐     ┌─────────────────┐
├─────────────────┤  ├────▶│  AgentShield     │────▶│  Sigma Rules    │
│  Claude Code    │  │     │  Engine (Go)     │     │  (YAML)         │
│  agentshield-cc │──┘     └──────────────────┘     └─────────────────┘
├─────────────────┤                │
│  Cursor, Codex  │                ▼
│  (coming soon)  │        ┌──────────────────┐
└─────────────────┘        │  LLM Triage      │
                           │  (via OpenClaw)  │
                           └──────────────────┘
```

AgentShield is **platform-agnostic** — one engine and one rule set, multiple integrations:

1. **Platform extensions** capture AI agent tool calls (OpenClaw, Claude Code, more coming)
2. **AgentShield Engine** evaluates events against Sigma rules in real-time  
3. **Sigma Rules** define detection patterns for AI-specific threats
4. **LLM Triage** automatically classifies alerts using OpenClaw loopback (no extra API keys required)

## Key Features

- **36+ Sigma detection rules** for AI agent threats across 12 MITRE ATT&CK categories
- **Single Go binary** — zero dependencies, easy deployment
- **Three evaluation modes**: enforce (block), audit (log), shadow (silent)
- **LLM-powered triage** via OpenClaw loopback (no extra API keys needed)
- **Feedback loop** with automatic rule refinement based on false positives
- **Hot rule reload** (SIGHUP signal) without service restart
- **SQLite alert storage** for persistence and analysis
- **CLI for management** (status, alerts, refine commands)
- **Real-time detection** with microsecond latency
- **HTTP API** for integration with external systems

## Quick Start

### Install via OpenClaw

```bash
# Install AgentShield plugin
/agentshield install
```

### Manual Installation

```bash
# 1. Clone the main repository
git clone https://github.com/agentshield-ai/agentshield.git
cd agentshield

# 2. Get the engine and rules
git clone https://github.com/agentshield-ai/agentshield-engine.git
git clone https://github.com/agentshield-ai/agentshield-rules.git

# 3. Build the engine
cd agentshield-engine
go build ./cmd/agentshield/

# 4. Start the engine
./agentshield serve -rules ../agentshield-rules/rules -port 8432

# 5. Install the OpenClaw plugin
cd ../plugin
cp -r . ~/.openclaw/plugins/agentshield/
```

## Configuration Example

```yaml
# ~/.agentshield/config.yaml
engine:
  host: "localhost"
  port: 8432
  mode: "audit"  # enforce, audit, shadow

rules:
  path: "~/.agentshield/rules"
  auto_reload: true

triage:
  provider: "openclaw"  # openclaw, openai, anthropic
  model: "claude-3-5-sonnet"
  auto_approve_threshold: 0.9

storage:
  path: "~/.agentshield/alerts.db"
  retention_days: 30

notifications:
  enabled: true
  channels: ["desktop"]
```

## Detection Categories

AgentShield includes comprehensive detection across all AI agent attack vectors:

| Category | Rules | Examples |
|----------|--------|----------|
| **Prompt Injection** | 3 | Direct jailbreaks, indirect manipulation |
| **Tool Poisoning** | 2 | MCP manipulation, skill tampering |  
| **Defense Evasion** | 8 | Memory poisoning, config manipulation |
| **Credential Access** | 3 | SSH keys, cloud credentials, env files |
| **Exfiltration** | 5 | Steganographic, DNS tunneling, network |
| **Privilege Escalation** | 4 | Container escape, IAM escalation |
| **Execution** | 3 | RCE attempts, dangerous commands |
| **Persistence** | 3 | Backdoors, rule tampering |
| **Discovery** | 2 | Network recon, DNS enumeration |
| **Lateral Movement** | 1 | Credential stuffing, pivot attempts |
| **Collection** | 1 | Suspicious file operations |
| **Initial Access** | 1 | Untrusted skill installation |

## Repository Structure

This organization contains three repositories that work together:

### 🛡️ [agentshield](https://github.com/agentshield-ai/agentshield) (This Repository)
Main project page — documentation, architecture, blog, and integration guides.

### ⚡ [agentshield-engine](https://github.com/agentshield-ai/agentshield-engine)  
Go detection engine built on a fork of RunReveal's sigmalite. HTTP API, CLI, LLM triage, feedback loop. Single binary, zero dependencies.

### 📋 [agentshield-rules](https://github.com/agentshield-ai/agentshield-rules)
39 Sigma detection rules across 12 MITRE ATT&CK categories.

### 🔌 Platform Extensions
| Repo | Platform | Status |
|------|----------|--------|
| [agentshield-openclaw](https://github.com/agentshield-ai/agentshield-openclaw) | OpenClaw | ✅ Production |
| [agentshield-claude](https://github.com/agentshield-ai/agentshield-claude) | Claude Code | 🚧 In Development |
| agentshield-cursor | Cursor | 📋 Planned |
| agentshield-codex | Codex | 📋 Planned |

## Getting Help

- **Documentation**: [docs/](./docs/) directory
- **Bug Reports**: [GitHub Issues](https://github.com/agentshield-ai/agentshield/issues)
- **Feature Requests**: [Discussions](https://github.com/agentshield-ai/agentshield/discussions)
- **Security Issues**: security@agentshield.ai

## Contributing

We welcome contributions! Please see our [Contributing Guide](./docs/contributing.md) for details.

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Business Model

AgentShield follows an **open core model**:

- **Open Source**: Detection engine, rules, and core functionality (Apache 2.0)
- **Pro Tier** (Optional): AgentShield Foundation Model for advanced threat intelligence, rule generation, and enterprise features

The entire core platform remains free and open source. Pro features enhance but never replace the open source functionality.

## License

Apache 2.0 - See [LICENSE](LICENSE) file for details.

## Acknowledgments

- [OpenClaw](https://github.com/openclaw-ai/openclaw) - AI agent framework
- [Sigma](https://github.com/SigmaHQ/sigma) - Detection rule format
- [Sigmalite](https://github.com/runreveal/sigmalite) - Go Sigma engine (Apache 2.0)
- MITRE ATT&CK - Threat taxonomy

---

**Protecting AI agents from adversarial attacks, one rule at a time.**