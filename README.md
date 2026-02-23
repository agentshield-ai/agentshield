# AgentShield

Modern AI agent security monitoring platform with real-time threat detection using Sigma rules.

![Go Version](https://img.shields.io/badge/Go-1.24+-blue)
![License](https://img.shields.io/badge/License-Apache%202.0-green)
![Build Status](https://img.shields.io/badge/Build-Passing-brightgreen)

## Overview

AgentShield is a comprehensive security monitoring solution designed specifically for AI agents. It provides real-time threat detection, intelligent triage, and seamless integration with agent platforms like OpenClaw and Claude Code.

## What AgentShield Is

AgentShield protects AI agents by:
- **Monitoring tool usage** in real-time with microsecond latency
- **Detecting threats** using community-maintained Sigma rules
- **Intelligent triage** with AI-powered false positive reduction
- **Enforcing policies** with block/audit/shadow modes
- **Integrating seamlessly** with existing agent workflows

## Architecture Overview

```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│   Plugins   │────│   Engine    │────│    Rules    │
│ (Platforms) │    │ (Detection) │    │ (Threats)   │
└─────────────┘    └─────────────┘    └─────────────┘
      │                    │                   │
   Collect              Evaluate           Patterns
   Events               & Triage           & Logic
```

## Components

### 🔧 Go Engine ([`cmd/`](cmd/) • [`internal/`](internal/) • [`pkg/sigma/`](pkg/sigma/))
High-performance detection engine built in Go with Chi HTTP server:
- Real-time Sigma rule evaluation using forked sigmalite
- Two-tier intelligent triage (fast + deep analysis)
- Multiple evaluation modes (enforce/audit/shadow)
- SQLite storage with automatic cleanup
- Hot rule reloading with zero downtime

**Quick Start:**
```bash
go build ./cmd/agentshield/
./agentshield serve -rules ./rules -config config.yaml
```

### 🔌 OpenClaw Plugin ([`plugins/openclaw/`](plugins/openclaw/))
TypeScript integration for OpenClaw agents:
- Real-time tool monitoring and evaluation
- Configurable enforcement modes
- Seamless workflow integration
- Event batching and async processing

**Installation:**
```bash
cd plugins/openclaw/
npm install && npm run build
openclaw plugin install ./dist/agentshield-plugin.js
```

### 🤖 Claude Code Hooks ([`plugins/claude/`](plugins/claude/))
Bash scripts for Claude Code CLI integration:
- Pre/post execution hooks
- Command line analysis
- Security policy enforcement
- Lightweight shell-based monitoring

**Setup:**
```bash
# Copy hooks to Claude Code directory
cp plugins/claude/hooks/* ~/.claude-code/hooks/
chmod +x ~/.claude-code/hooks/*
```

### 📊 Detection Rules ([`rules/`](rules/))
AgentShield consumes engine-agnostic Sigma-AI rules from the upstream catalog (`agentshield-ai/sigma-ai`) vendored under [`rules/upstream/sigma-ai/`](rules/upstream/sigma-ai/).

Community-maintained Sigma rules for AI agent threats:
- **Prompt Injection**: Social engineering and manipulation detection
- **Tool Poisoning**: Malicious tool usage patterns
- **Data Exfiltration**: Unauthorized data access attempts
- **Privilege Escalation**: Unauthorized system access
- **Credential Access**: Token theft and authentication bypass

Browse rules: [`rules/`](rules/)

### 📚 Documentation ([`docs/`](docs/))
Complete documentation for deployment and usage:
- [**API Reference**](docs/api.md) - HTTP endpoints and examples
- [**Configuration**](docs/configuration.md) - Complete config options
- [**Deployment Guide**](docs/deployment.md) - Production setup
- [**Triage System**](docs/triage.md) - AI-powered alert analysis
- [**Quick Start**](docs/QUICK_START.md) - Get running in minutes

## Quick Start

### 1. Build the Engine
```bash
git clone https://github.com/agentshield-ai/agentshield.git
cd agentshield
go build ./cmd/agentshield/
```

### 2. Start Monitoring
```bash
# Basic setup
./agentshield serve -rules ./rules

# With configuration
./agentshield serve -config config.yaml
```

### 3. Install Platform Plugin

**OpenClaw:**
```bash
cd plugins/openclaw/
npm install && openclaw plugin install .
```

**Claude Code:**
```bash
cp plugins/claude/hooks/* ~/.claude-code/hooks/
```

### 4. View Alerts
```bash
./agentshield alerts list
curl http://localhost:8433/api/v1/alerts
```

## Key Features

- **🚀 Microsecond Latency**: High-performance Go engine with Chi router
- **🧠 Intelligent Triage**: AI-powered false positive reduction  
- **🔄 Hot Reloading**: Update rules without downtime
- **🌐 Multi-Platform**: OpenClaw, Claude Code, and extensible plugin system
- **📈 Production Ready**: SQLite storage, structured logging, graceful shutdown
- **🔒 Security First**: Token authentication, input validation, safe defaults

## Configuration Example

```yaml
server:
  port: 8433
auth:
  token: "${AGENTSHIELD_AUTH_TOKEN}"
rules:
  dir: "./rules"
  hot_reload: true
evaluation_mode: "audit"  # enforce, audit, shadow
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
```

## Development

```bash
# Run tests
go test ./...

# Debug mode
./agentshield serve -log-level debug

# Contribute rules
cp my-rule.yml rules/custom/
git commit -m "feat: add custom threat detection"
```

## Community

- **Canonical Rules Repository**: [sigma-ai](https://github.com/agentshield-ai/sigma-ai) - Engine-agnostic AI-agent Sigma rules
- **Vendored Upstream Snapshot**: [`rules/upstream/sigma-ai/`](rules/upstream/sigma-ai/) - Imported into this engine repo via subtree
- **Plugin Development**: [plugins/](plugins/) - Platform integrations
- **Documentation**: [docs/](docs/) - Comprehensive guides

## Support

- **GitHub Issues**: Bug reports and feature requests
- **Discussions**: Architecture and usage questions  
- **Security**: security@agentshield.ai

## License

Apache 2.0 - See [LICENSE](LICENSE) for details.

Built on RunReveal's sigmalite (Apache 2.0) with enhancements for AI agent security.