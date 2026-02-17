# AgentShield - AI Agent Security Monitoring

AgentShield is a local SIEM-lite for AI agent security monitoring. It monitors AI agent activity, detects threats using Sigma rules, and provides real-time security evaluation for tool calls.

## What It Does

- **Real-Time Monitoring**: Intercepts and evaluates tool calls before execution with <50ms latency
- **Sigma Detection**: Uses 18+ industry-standard Sigma rules to detect security threats
- **LLM Triage**: Automatically classifies alerts using Claude with extended reasoning  
- **Threat Categories**: Detects RCE, credential access, data exfiltration, privilege escalation, and more
- **Feedback Learning**: Improves detection accuracy based on user feedback
- **Desktop Notifications**: Alerts you to security events via system notifications

## How to Use

### Basic Commands

Monitor in foreground (see activity in real-time):
```bash
agentshield start -f
```

View recent alerts:
```bash
agentshield alerts
agentshield alerts --level high
```

Generate security summary:
```bash
agentshield summary --days 7
```

### Real-Time Integration

The AgentShield plugin automatically:
- Evaluates tool calls like `exec`, `write`, `browser`, `message` before execution
- Returns `allow`, `block`, or `log` decisions
- Logs all activity for historical analysis

### Check Alert Status

When AgentShield detects suspicious activity:

1. **Check recent alerts**:
   ```bash
   agentshield alerts --limit 10
   ```

2. **Review high-priority events**:
   ```bash
   agentshield alerts --level critical
   agentshield alerts --level high
   ```

3. **Get detailed security summary**:
   ```bash
   agentshield summary
   ```

### Configure Detection

List active detection rules:
```bash
agentshield rules --stats
```

Refine rules with high false positives:
```bash
agentshield refine
agentshield refine <rule-id> --apply
```

### Service Management

Check if monitoring is active:
```bash
agentshield status
```

Install as system service (auto-start):
```bash
agentshield install-service
```

Stop monitoring:
```bash
agentshield stop
```

## Configuration

AgentShield configuration is in `~/.agentshield/config.yaml`. Key settings:

- **LLM Triage**: Requires `ANTHROPIC_API_KEY` environment variable
- **Alert Levels**: Critical, High, Medium, Low thresholds
- **Real-time Settings**: Timeout, auth, circuit breaker config
- **Retention**: How long to keep alert history (default: 90 days)

## Understanding Alerts

### Alert Levels
- **Critical**: Immediate action required (RCE attempts, system compromise)
- **High**: Serious threats (credential access, privilege escalation)
- **Medium**: Suspicious activity (unusual commands, network activity)  
- **Low**: Informational (minor anomalies, reconnaissance)

### Common Alert Types
- **RCE Injection**: Prompt injection leading to code execution
- **Credential Access**: Reading SSH keys, passwords, API tokens
- **Data Exfiltration**: Suspicious network transfers or file operations
- **Persistence**: Attempts to install backdoors or maintain access
- **Container Escape**: Breaking out of containerized environments

### Response Actions
1. **Review the alert details** - Check what triggered the detection
2. **Verify legitimacy** - Was this an expected operation?
3. **Investigate impact** - What data/systems might be affected?
4. **Take containment action** - Block, isolate, or terminate if needed
5. **Provide feedback** - Help AgentShield learn from false positives

## Troubleshooting

### Performance
- Real-time evaluation has 50ms timeout by default
- If experiencing latency, check `agentshield status`
- Monitor system resources during heavy AI agent activity

### False Positives
- Use `agentshield refine` to tune overly sensitive rules
- Provide feedback on incorrect alerts to improve accuracy
- Check rule statistics with `agentshield rules --stats`

### Integration Issues
- Verify the AgentShield plugin is enabled in OpenClaw config
- Check real-time server is running: `agentshield status`
- Test connectivity: `curl http://localhost:8432/api/v1/health`

## Privacy & Security

- **Local Processing**: All monitoring happens locally, no data sent to external services
- **Encrypted Storage**: Alert database uses local SQLite with proper permissions
- **Configurable Scope**: Choose which tool calls to monitor via intercept/skip lists
- **Audit Trail**: Complete history of all security evaluations and decisions

AgentShield enhances your AI agent security without compromising privacy or performance.