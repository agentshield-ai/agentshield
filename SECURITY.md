# Security Policy

## Supported Versions

Current supported branch for security fixes:
- `main` (latest)

## Reporting a Vulnerability

Please do **not** open public issues for suspected vulnerabilities.

Report privately via:
- GitHub Security Advisories (preferred)
- Or contact maintainers directly if advisory flow is unavailable

Include:
- Affected version/commit
- Reproduction steps / PoC
- Impact assessment
- Suggested mitigation (if known)

## Disclosure Process

1. We acknowledge receipt as quickly as possible.
2. We validate and triage severity.
3. We prepare a fix and coordinated disclosure.
4. We publish a changelog/security advisory.

## Security Notes

- AgentShield should be run with strong auth tokens and least-privilege network exposure.
- Treat all inbound event content as untrusted.
- Prefer fail-closed modes where operationally acceptable.
