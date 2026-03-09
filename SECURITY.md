# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in AgentShield, please report it responsibly.

**Email:** [security@agentshield.ai](mailto:security@agentshield.ai)

Please include:

- A description of the vulnerability
- Steps to reproduce the issue
- The potential impact
- Any suggested fix (optional)

## Response Timeline

- **Acknowledgement:** within 48 hours
- **Initial assessment:** within 5 business days
- **Fix or mitigation:** depends on severity, but we aim for 30 days for critical issues

## Scope

This policy covers:

- The AgentShield Go engine (`cmd/`, `internal/`, `pkg/`)
- The OpenClaw plugin (`plugins/openclaw/`)
- The Claude Code hook (`plugins/claude/`)
- Sigma detection rules (`rules/`)
- Installation and deployment scripts (`scripts/`, `plugins/openclaw/skill/`)

## Disclosure

We follow coordinated disclosure. We ask that you do not publicly disclose the vulnerability until we have released a fix or 90 days have passed since your report, whichever comes first.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | Best effort |
