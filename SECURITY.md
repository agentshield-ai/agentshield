# Security Policy

This document describes how to report security vulnerabilities in AgentShield and what to expect from the disclosure process.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.x     | Yes       |

## Reporting a Vulnerability

Please report security vulnerabilities by email to **security@agentshield.ai**.

- Do **not** open a public GitHub issue for security vulnerabilities.
- You should receive an acknowledgement within 48 hours.
- We aim to release a fix within 7 days of confirmation.
- If you do not receive a response within 48 hours, please follow up to ensure the original message was received.

## Scope

AgentShield is a detection and monitoring tool for AI agents. Security reports may cover:

- Authentication bypass in the HTTP API
- Rule evaluation logic errors that allow evasion (i.e., crafted inputs that circumvent detection rules)
- Input validation failures leading to injection attacks
- Information disclosure in API responses or logs
- Dependency vulnerabilities with demonstrable exploitable impact

Reports concerning detection rule coverage gaps (e.g., a novel attack technique not yet covered by a Sigma rule) are welcome but are better suited to a standard GitHub issue or discussion, as they do not constitute vulnerabilities in the engine itself.

## Responsible Disclosure

We follow a coordinated disclosure model. We ask that reporters refrain from publicly disclosing vulnerability details until a fix has been released and a reasonable window (typically 90 days) has elapsed for users to update.
