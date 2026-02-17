# AgentShield

**AI Agent Detection & Response** - A comprehensive security framework for monitoring and protecting AI agents against adversarial attacks.

![AgentShield Architecture](https://img.shields.io/badge/Architecture-Plugin%20%E2%86%92%20Engine%20%E2%86%92%20Rules-blue)
![License](https://img.shields.io/badge/License-Apache%202.0-green)
![Status](https://img.shields.io/badge/Status-Production%20Ready-brightgreen)

## 🛡️ What is AgentShield?

AgentShield is a real-time security monitoring and detection system specifically designed for AI agents. It provides:

- **Real-time threat detection** using Sigma rules
- **Behavioral monitoring** of agent actions and tool usage  
- **Prompt injection detection** for both direct and indirect attacks
- **Tool poisoning prevention** and MCP security monitoring
- **Credential access protection** and data exfiltration detection
- **Integration with OpenClaw** for seamless agent security

## 🏗️ Architecture

AgentShield follows a modular architecture with three main components:

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   AgentShield   │───▶│ AgentShield      │───▶│ AgentShield     │
│   Plugin        │    │ Engine           │    │ Rules           │
│                 │    │                  │    │                 │
│ • OpenClaw      │    │ • Sigma Runtime  │    │ • 36+ Rules     │
│   Integration   │    │ • HTTP API       │    │ • MITRE ATT&CK  │
│ • Event         │    │ • Rule Engine    │    │ • AI-Specific   │
│   Collection    │    │ • Go Performance │    │   Detections    │
│ • Skill System  │    │ • Real-time Eval │    │ • Categories    │
└─────────────────┘    └──────────────────┘    └─────────────────┘
```

### Components

1. **[AgentShield Plugin](./plugin/)** - OpenClaw integration and event collection
2. **[AgentShield Engine](https://github.com/agentshield-ai/agentshield-engine)** - High-performance Go detection engine
3. **[AgentShield Rules](https://github.com/agentshield-ai/agentshield-rules)** - Sigma rules for AI agent threats

## 🚀 Quick Start

### Install via OpenClaw

```bash
# Install the AgentShield plugin
openclaw plugin install agentshield

# Add the skill to your agent
openclaw skill add agentshield
```

### Manual Installation

1. **Clone the repository:**
   ```bash
   git clone https://github.com/agentshield-ai/agentshield.git
   cd agentshield
   ```

2. **Install the plugin:**
   ```bash
   # Copy plugin to OpenClaw plugins directory
   cp -r plugin/ ~/.openclaw/plugins/agentshield/
   
   # Install the skill
   cp -r skill/ ~/.openclaw/skills/agentshield/
   ```

3. **Start the detection engine:**
   ```bash
   # Get the engine and rules
   git clone https://github.com/agentshield-ai/agentshield-engine.git
   git clone https://github.com/agentshield-ai/agentshield-rules.git
   
   # Build and run
   cd agentshield-engine
   make build
   ./bin/agentshield -rules ../agentshield-rules/rules
   ```

## 📊 Detection Capabilities

AgentShield detects 36+ different attack patterns across 12 MITRE ATT&CK categories:

| Category | Examples | Rule Count |
|----------|----------|------------|
| **Prompt Injection** | Direct jailbreaks, indirect manipulation | 3 |
| **Tool Poisoning** | MCP manipulation, skill tampering | 2 |  
| **Credential Access** | SSH keys, cloud credentials, env files | 3 |
| **Data Exfiltration** | Steganographic, DNS tunneling, network | 5 |
| **Privilege Escalation** | Container escape, IAM escalation | 4 |
| **Defense Evasion** | Memory poisoning, config manipulation | 7 |
| **Execution** | RCE attempts, dangerous commands | 3 |
| **Persistence** | Backdoors, rule tampering | 3 |
| **Discovery** | Network recon, DNS enumeration | 2 |
| **Lateral Movement** | Credential stuffing, pivot attempts | 1 |
| **Collection** | Suspicious file operations | 1 |
| **Initial Access** | Untrusted skill installation | 1 |

## 📖 Documentation

### Getting Started
- [Installation Guide](./docs/installation.md)
- [Configuration](./docs/configuration.md) 
- [Quick Start Tutorial](./docs/quickstart.md)

### Architecture & Design
- [System Architecture](./docs/architecture.md)
- [Plugin Development](./docs/plugin-development.md)
- [Rule Creation](./docs/rule-creation.md)

### Advanced Usage
- [API Reference](./docs/api-reference.md)
- [Custom Rules](./docs/custom-rules.md)
- [Integration Guide](./docs/integration.md)

## 🔗 Related Repositories

| Repository | Description | Language |
|------------|-------------|----------|
| **[agentshield](https://github.com/agentshield-ai/agentshield)** | Main plugin and documentation | Python |
| **[agentshield-engine](https://github.com/agentshield-ai/agentshield-engine)** | High-performance detection engine | Go |
| **[agentshield-rules](https://github.com/agentshield-ai/agentshield-rules)** | Sigma rules for AI agent threats | YAML |

## 🛠️ Development

### Prerequisites
- Python 3.8+
- Go 1.21+ (for engine development)
- OpenClaw framework
- Docker (optional)

### Local Development

```bash
# Clone all repositories
git clone https://github.com/agentshield-ai/agentshield.git
git clone https://github.com/agentshield-ai/agentshield-engine.git  
git clone https://github.com/agentshield-ai/agentshield-rules.git

# Set up development environment
cd agentshield
python -m venv venv
source venv/bin/activate
pip install -r plugin/requirements.txt

# Run tests
python -m pytest plugin/tests/
```

### Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## 📄 Blog & Research

Read our launch blog post: [Introducing AgentShield](./blog_post.md)

## 🐛 Issues & Support

- **Bug Reports**: [GitHub Issues](https://github.com/agentshield-ai/agentshield/issues)
- **Feature Requests**: [Discussions](https://github.com/agentshield-ai/agentshield/discussions)
- **Security Issues**: security@agentshield.ai

## 📜 License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- [OpenClaw](https://github.com/openclaw-ai/openclaw) - AI agent framework
- [Sigma](https://github.com/SigmaHQ/sigma) - Detection rule format
- [Sigmalite](https://github.com/runreveal/sigmalite) - Go Sigma engine
- MITRE ATT&CK - Threat taxonomy

---

**AgentShield** - Protecting AI agents from adversarial attacks, one rule at a time.