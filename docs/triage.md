# Triage System

AgentShield Engine features an intelligent two-tier triage system that combines fast synchronous analysis with deep asynchronous investigation using AI agents.

## Architecture Overview

```
Event → Rule Engine → Alert Generated
                         ↓
                   [Severity Check]
                         ↓
              High/Critical Alerts Only
                         ↓
                 ┌─────────────────┐
                 │   FAST TRIAGE   │ ← Synchronous (~4s)
                 │  (In request)   │   OpenAI/Anthropic API
                 └─────────────────┘
                         ↓
              ┌─────────────────────┐
              │   DEEP TRIAGE      │ ← Asynchronous (background)
              │ (OpenClaw Agent)   │   Tool access, reasoning
              └─────────────────────┘
                         ↓
                 Results delivered via:
                 • Chat messages
                 • Webhook notifications
                 • Database storage
```

## Two-Tier System

### 1. Fast Triage (Synchronous)

**Purpose**: Provide immediate threat assessment for high-priority alerts within the request lifecycle.

**Characteristics**:
- **Speed**: ~4 seconds response time
- **Scope**: In-request processing
- **Triggers**: High and Critical severity alerts only
- **Providers**: OpenAI or Anthropic APIs
- **Context**: Limited to alert data and recent events

**When It Triggers**:
```yaml
# Configuration
triage:
  enabled: true
  # Automatically triggers for alerts with severity >= high
```

**Flow**:
1. Alert generated with high/critical severity
2. Fast triage provider called synchronously
3. LLM analyzes alert context and provides verdict
4. Result included in evaluation response
5. Fallback to "unknown" verdict if triage fails

### 2. Deep Triage (Asynchronous)

**Purpose**: Comprehensive investigation using AI agents with tool access for complex threat analysis.

**Characteristics**:
- **Speed**: Variable (30s - 5 minutes)
- **Scope**: Background processing, no request blocking
- **Triggers**: Configurable minimum severity (default: critical only)
- **Provider**: OpenClaw sub-agent with tool access
- **Context**: Full investigation capabilities

**When It Triggers**:
```yaml
# Configuration
deep_triage:
  enabled: true
  min_severity: "critical"  # Only critical alerts trigger deep analysis
```

**Flow**:
1. Alert meets severity threshold for deep triage
2. OpenClaw sub-agent spawned in background
3. Agent investigates using available tools
4. Results delivered asynchronously via webhook/chat
5. Database updated with investigation findings

## Fast Triage Configuration

### Provider Setup

#### OpenAI Provider
```yaml
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"
  max_tokens: 500
  timeout_sec: 10
```

#### Anthropic Provider
```yaml
triage:
  enabled: true
  provider: "anthropic" 
  model: "claude-3-5-sonnet-20241022"
  api_key: "${ANTHROPIC_API_KEY}"
  max_tokens: 500
  timeout_sec: 10
```

#### OpenRouter Integration
```yaml
triage:
  enabled: true
  provider: "openai"  # Use OpenAI-compatible API
  model: "anthropic/claude-3.5-sonnet"  # OpenRouter model name
  api_key: "${OPENROUTER_API_KEY}"
  base_url: "https://openrouter.ai/api/v1"
  max_tokens: 500
  timeout_sec: 15
```

### Prompt Engineering

Fast triage uses a structured prompt to ensure consistent analysis:

```text
You are a cybersecurity analyst reviewing an AI agent security alert.

ALERT DETAILS:
Rule: {rule_name}
Severity: {severity}
Event: {event_summary}

CONTEXT:
Recent Events: {recent_events}
Session Info: {session_context}

TASK:
Determine if this is a TRUE_POSITIVE (real threat) or FALSE_POSITIVE (benign).

Respond with JSON:
{
  "verdict": "TRUE_POSITIVE" | "FALSE_POSITIVE", 
  "confidence": 0.0-1.0,
  "reasoning": "Brief explanation"
}
```

### Verdict Integration

Fast triage verdicts are incorporated into the evaluation response:

```json
{
  "action": "BLOCK",
  "alerts": [...],
  "triage_result": {
    "verdict": "TRUE_POSITIVE",
    "confidence": 0.95,
    "reasoning": "Command matches known attack patterns",
    "provider": "openai",
    "model": "gpt-4o-mini",
    "processing_time_ms": 4200
  }
}
```

### Fallback Behavior

When fast triage fails:
1. **Network Error**: Log error, continue with default action
2. **Timeout**: Log timeout, continue with default action  
3. **Invalid Response**: Log parsing error, continue with default action
4. **Rate Limited**: Log rate limit, continue with default action

```go
// Example fallback behavior
if triageResult == nil {
    logger.Warn("Fast triage failed, using default action")
    triageResult = &TriageResult{
        Verdict: "UNKNOWN",
        Confidence: 0.0,
        Reasoning: "Triage unavailable",
    }
}
```

## Deep Triage Configuration

### OpenClaw Integration

Deep triage requires an OpenClaw instance with agent capabilities:

```yaml
deep_triage:
  enabled: true
  gateway_url: "http://127.0.0.1:18789"
  gateway_token: "${OPENCLAW_GATEWAY_TOKEN}"
  min_severity: "critical"
  
  agent:
    system_prompt: |
      You are an expert SOC analyst investigating AI agent security alerts.
      
      Your mission:
      1. Determine if the alert represents a genuine security threat
      2. Gather additional context using available tools
      3. Provide actionable recommendations
      
      Investigation approach:
      - Search for similar attack patterns online
      - Check threat intelligence databases
      - Analyze command/file patterns for malicious intent
      - Consider the operational context
      
      Be thorough but efficient. Lives may depend on accurate analysis.
      
    model: "anthropic/claude-sonnet-4-20250514"
    thinking: "low"
    tools: 
      - "web_search"
      - "web_fetch"  
      - "memory_search"
    timeout_sec: 120
```

### Agent Personality Customization

Create specialized SOC analysts with custom prompts:

#### Malware Analysis Specialist
```yaml
agent:
  system_prompt: |
    You are a malware analysis specialist focused on AI agent threats.
    
    Expertise areas:
    - Command injection patterns
    - Fileless malware techniques  
    - Living-off-the-land binaries (LOLBins)
    - AI prompt injection attacks
    - Data exfiltration methods
    
    For each alert, determine:
    1. Attack vector and technique
    2. Potential impact and scope
    3. Recommended containment actions
    4. IOCs for threat hunting
```

#### Cloud Security Expert
```yaml
agent:
  system_prompt: |
    You are a cloud security expert specializing in AI agent deployments.
    
    Focus on:
    - Cloud service abuse (AWS, Azure, GCP)
    - Container escape techniques
    - IAM privilege escalation
    - API key compromise
    - Lateral movement patterns
    
    Consider cloud-native context in your analysis.
```

### Tool Configuration

Enable specific investigation tools based on your environment:

```yaml
agent:
  tools:
    - "web_search"      # Threat intelligence searches
    - "web_fetch"       # Fetch suspicious URLs/files  
    - "memory_search"   # Search agent conversation history
    # - "exec"          # Execute investigation commands (careful!)
    # - "read"          # Read system files (careful!)
```

**Tool Descriptions**:
- `web_search`: Search the web for threat intelligence, CVEs, attack patterns
- `web_fetch`: Fetch and analyze suspicious URLs, download samples
- `memory_search`: Search through agent memory/conversation history for context
- `exec`: Execute system commands (high risk - use cautiously)
- `read`: Read files and configurations (medium risk)

### Severity Thresholds

Configure when deep triage triggers:

```yaml
deep_triage:
  min_severity: "high"    # Triggers for high and critical
  # min_severity: "critical"  # Only critical alerts (default)
  # min_severity: "medium"    # More comprehensive but expensive
  # min_severity: "low"       # All alerts (very expensive)
```

## Result Delivery

### Webhook Notifications

Configure webhooks to receive deep triage results:

```yaml
deep_triage:
  webhook: "https://your-soc.com/api/webhooks/agentshield"
```

**Webhook Payload**:
```json
{
  "event_type": "deep_triage_complete",
  "timestamp": "2024-01-15T12:30:00Z",
  "alert": {
    "id": 123,
    "rule_id": "agent-dangerous-commands-001",
    "severity": "critical",
    "event_id": "evt-abc-123"
  },
  "triage_result": {
    "verdict": "TRUE_POSITIVE", 
    "confidence": 0.98,
    "reasoning": "Confirmed APT-style living-off-the-land attack using PowerShell",
    "investigation_summary": "Agent performed web search for 'powershell empire framework' and confirmed this matches documented attack patterns from recent threat reports.",
    "recommendations": [
      "Immediately isolate affected system",
      "Hunt for similar PowerShell activity",
      "Review recent network connections",
      "Check for persistence mechanisms"
    ],
    "iocs": [
      "powershell.exe -nop -w hidden -c \"IEX(New-Object...)",
      "Empire PowerShell framework indicators"
    ],
    "tools_used": ["web_search", "web_fetch"],
    "investigation_time_sec": 87,
    "agent_session": "deep-triage-abc123"
  }
}
```

### Chat Integration

Results can be delivered via chat platforms:

```yaml
# OpenClaw configuration for chat delivery
deep_triage:
  agent:
    # Results automatically sent to configured chat channels
    # when investigation completes
```

### Database Storage

All triage results are stored in SQLite for historical analysis:

```sql
-- Query deep triage results
SELECT 
    a.rule_id,
    a.severity,
    dt.verdict,
    dt.confidence,
    dt.investigation_summary,
    dt.created_at
FROM alerts a
JOIN deep_triage_results dt ON a.id = dt.alert_id  
WHERE dt.created_at > datetime('now', '-7 days')
ORDER BY dt.created_at DESC;
```

## Cost Considerations

### Fast Triage Costs
- **OpenAI GPT-4o-mini**: ~$0.001 per alert
- **Anthropic Claude Haiku**: ~$0.002 per alert  
- **OpenRouter**: Variable, often 50-80% cheaper than direct APIs
- **OpenClaw**: Uses your existing OpenClaw credits

### Deep Triage Costs
- **Only fires for high-priority alerts** (default: critical only)
- **Typical investigation**: 2-5 minutes of agent time
- **With tools**: Additional API costs for web searches (~$0.01-0.05)
- **Budget control**: Configure `min_severity` to control frequency

### Cost Optimization

```yaml
# Cost-conscious configuration
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"  # Cheapest option
  base_url: "https://openrouter.ai/api/v1"  # Often cheaper

deep_triage:
  enabled: true
  min_severity: "critical"  # Only highest priority
  agent:
    thinking: "off"  # Reduce token usage
    timeout_sec: 60   # Shorter investigations
```

## Performance Tuning

### Fast Triage Optimization
```yaml
triage:
  max_tokens: 200      # Reduce for faster responses
  timeout_sec: 5       # Aggressive timeout
  # Use faster models: gpt-4o-mini, claude-haiku
```

### Deep Triage Optimization
```yaml
deep_triage:
  agent:
    timeout_sec: 30    # Faster investigations
    tools: ["web_search"]  # Fewer tools = faster analysis
```

### Monitoring Performance

```bash
# Monitor triage response times
curl -s http://localhost:8433/api/v1/health | jq '.performance'

# Check recent alerts via API
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8433/api/v1/alerts?limit=10" | jq '.alerts'
```

## Troubleshooting

### Fast Triage Issues

**Symptom**: Slow evaluation responses
```bash
# Check triage timeout configuration
grep -A5 "triage:" config.yaml
```

**Symptom**: "Triage unavailable" verdicts
```bash  
# Check API key and connectivity
curl -H "Authorization: Bearer $OPENAI_API_KEY" \
  https://api.openai.com/v1/models
```

### Deep Triage Issues

**Symptom**: No deep triage results
```bash
# Check OpenClaw connectivity
curl -H "Authorization: Bearer $OPENCLAW_GATEWAY_TOKEN" \
  http://localhost:18789/api/v1/health
```

**Symptom**: Agent timeouts
```yaml
# Increase timeout in config
deep_triage:
  agent:
    timeout_sec: 180  # 3 minutes
```

### Debugging Tools

```bash
# Enable debug logging
AGENTSHIELD_LOG_LEVEL=debug ./agentshield serve --config config.yaml

# Monitor triage performance
tail -f /var/log/agentshield/engine.log | grep "triage"
```

## Best Practices

### 1. Gradual Rollout
```yaml
# Start with fast triage only
triage:
  enabled: true

# Add deep triage for critical alerts only
deep_triage:
  enabled: true
  min_severity: "critical"

# Expand as needed
deep_triage:
  min_severity: "high"
```

### 2. Custom Prompts
- **Be specific** about your environment and threat model
- **Include examples** of common false positives in your context
- **Update regularly** based on feedback and new threat patterns
- **Test prompts** with historical alerts to verify effectiveness

### 3. Cost Management
- **Monitor usage** regularly via API costs and OpenClaw credits
- **Adjust thresholds** based on alert volume and budget
- **Use cheaper models** for fast triage (gpt-4o-mini, claude-haiku)
- **Optimize prompts** to reduce token usage

### 4. Integration Planning
- **Webhook endpoints** should handle failures gracefully
- **Database queries** should include proper indexing for performance
- **Chat integrations** should filter noise for human analysts
- **Feedback loops** should capture analyst corrections for continuous improvement

The two-tier triage system provides both immediate threat assessment and deep investigative capabilities, allowing organizations to balance speed, accuracy, and cost based on their specific security requirements.