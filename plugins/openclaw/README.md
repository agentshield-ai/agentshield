# AgentShield OpenClaw Plugin

OpenClaw plugin for AgentShield - provides real-time security monitoring and threat detection for AI agents.

## Overview

This plugin integrates AgentShield's detection capabilities directly into OpenClaw, enabling real-time monitoring of agent tool calls and automatic blocking of malicious activity.

## Features

- **Real-time monitoring** of all OpenClaw tool calls
- **Automatic threat detection** using Sigma rules
- **Three evaluation modes**: enforce (block), audit (log), shadow (silent)
- **LLM-powered triage** for intelligent alert classification
- **Zero-config setup** with sensible defaults
- **Hot rule reloading** without plugin restart

## Installation

### Via OpenClaw Command

```bash
/agentshield install
```

### Manual Installation

1. Copy the plugin to your OpenClaw plugins directory:
```bash
cp -r plugin/ ~/.openclaw/plugins/agentshield/
```

2. Install the AgentShield engine:
```bash
# Get the engine
git clone https://github.com/agentshield-ai/agentshield-engine.git
cd agentshield-engine

# Build and start
go build ./cmd/agentshield/
./agentshield serve -port 8433
```

3. Get the detection rules:
```bash
git clone https://github.com/agentshield-ai/sigma-ai.git
```

## Configuration

The plugin automatically detects and connects to the AgentShield engine. Default configuration:

```json
{
  "engine": {
    "host": "localhost",
    "port": 8433,
    "timeout": 5000
  },
  "mode": "audit",
  "triage": {
    "enabled": true,
    "auto_approve_threshold": 0.9
  }
}
```

### Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `mode` | `audit` | Evaluation mode: `enforce`, `audit`, `shadow` |
| `engine.host` | `localhost` | AgentShield engine hostname |
| `engine.port` | `8433` | AgentShield engine port |
| `engine.timeout` | `5000` | Request timeout in milliseconds |
| `triage.enabled` | `true` | Enable LLM-powered triage |
| `triage.auto_approve_threshold` | `0.9` | Auto-approve false positives above this confidence |

## Usage

Once installed, the plugin automatically monitors all tool calls. No additional configuration required.

### Evaluation Modes

**Enforce Mode** (Production):
- Blocks malicious tool calls in real-time
- Shows security alerts to users
- Requires manual review for suspicious activity

**Audit Mode** (Default):
- Logs all activity without blocking
- Generates security alerts for review
- Safe for testing and monitoring

**Shadow Mode** (Baseline):
- Silent monitoring only
- Collects data for rule tuning
- No user-facing alerts

### Manual Controls

Users can interact with AgentShield through chat commands:

```
/agentshield status          - Show detection status
/agentshield alerts          - List recent alerts  
/agentshield mode enforce    - Change to enforce mode
/agentshield mode audit      - Change to audit mode
/agentshield feedback <id>   - Provide feedback on alert
```

## Development

### Prerequisites

- Node.js 18+
- TypeScript
- OpenClaw development environment

### Build

```bash
npm install
npm run build
```

### Testing

```bash
npm test
```

### Plugin Structure

```
plugin/
├── index.ts                 # Main plugin entry point
├── src/
│   ├── engine-client.ts     # AgentShield engine communication
│   ├── evaluator.ts         # Event evaluation logic
│   ├── formatter.ts         # Event formatting utilities
│   └── types.ts             # TypeScript type definitions
├── package.json
├── tsconfig.json
└── openclaw.plugin.json     # Plugin manifest
```

### Key Classes

**AgentShieldPlugin**: Main plugin class handling tool call interception
**EngineClient**: HTTP client for communicating with AgentShield engine  
**EventEvaluator**: Logic for evaluating tool calls against security rules
**AlertFormatter**: Utilities for formatting and displaying security alerts

## API Integration

The plugin communicates with the AgentShield engine via HTTP API:

### Evaluate Event
```typescript
POST /api/v1/evaluate
Content-Type: application/json

{
  "event_id": "unique-id",
  "timestamp": "2024-01-15T10:30:00Z",
  "event_type": "tool_call",
  "fields": {
    "tool": "exec",
    "command": "ls -la",
    "working_dir": "/home/user"
  }
}
```

### Response
```typescript
{
  "action": "ALLOW",        // ALLOW, BLOCK, LOG
  "alerts": [               // Triggered alerts
    {
      "rule_id": "rule-001",
      "severity": "medium",
      "verdict": "false_positive",
      "confidence": 0.95
    }
  ],
  "reason": "High confidence false positive"
}
```

## Security

- All communication with the engine uses HTTPS in production
- Sensitive data is never logged or transmitted
- Plugin runs with minimal privileges
- Local-first approach - no external dependencies

## Troubleshooting

### Engine Connection Issues

1. **Check engine status**:
```bash
./agentshield status
```

2. **Verify port availability**:
```bash
netstat -an | grep 8433
```

3. **Check logs**:
```bash
tail -f ~/.openclaw/logs/agentshield.log
```

### Performance Issues

- Reduce rule complexity for faster evaluation
- Increase engine timeout for complex rules
- Use shadow mode for high-volume testing

### Rule Issues

- Validate rules using: `./agentshield rules validate`
- Reload rules with: `kill -HUP <agentshield-pid>`
- Check rule syntax against Sigma specification

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests for new functionality
5. Submit a pull request

## License

Apache 2.0 - See [LICENSE](../LICENSE) for details.