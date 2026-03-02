# Documentation Review Design

**Date:** 2026-03-01
**Status:** Approved
**Objective:** Bring AgentShield documentation to best-in-class open-source standard

## Target Audiences

1. **General OpenClaw users** -- not necessarily security practitioners; need accessible, jargon-free explanations
2. **Developers** -- integrating with AgentShield via API or building plugins; need accurate, complete technical reference

## Review Criteria

| Criterion | Description |
|-----------|-------------|
| Language | British English throughout |
| Tone | Formal British academic, friendly and accessible |
| Scientific integrity | No unsubstantiated claims; performance assertions carry caveats |
| Accessibility | Security terminology defined on first use; concepts explained for non-specialists |
| Structure | Clear hierarchy with purpose statement, prerequisites, content, and cross-references |
| Accuracy | Code examples match actual API; config examples valid; file paths exist |
| Completeness | All features documented; no orphan references |
| Cross-references | Consistent linking; no dead links |

## Team Structure

### Agent 1: user-docs-reviewer

**Files:** README.md, PLATFORMS.md, plugins/openclaw/README.md, plugins/claude/README.md

**Focus:** Welcoming to non-security-expert OpenClaw users. Quickstart must be genuinely quick. Installation instructions complete and tested.

### Agent 2: technical-docs-reviewer

**Files:** docs/api.md, docs/architecture.md, docs/configuration.md, docs/evaluation.md, docs/rules.md, docs/triage.md, docs/testing-rules.md

**Focus:** API accuracy, code example correctness, architecture clarity, configuration completeness.

### Agent 3: ops-docs-reviewer

**Files:** CONTRIBUTING.md, SECURITY.md, CODE_OF_CONDUCT.md, docs/deployment.md, docs/log_rotation.md, docs/rules-source.md, docs/skills-abuse-eval.md, docs/README.md, rules/README.md, pkg/sigma/README.md

**Focus:** Contributor workflow clarity, production deployment readiness, operational completeness.

### Agent 4: consistency-reviewer (sequential, after agents 1--3)

**Scope:** All changes from agents 1--3

**Focus:** Terminology consistency, British English, formatting standards, cross-reference integrity, tone uniformity.

## Out of Scope

- Design documents (`design/` directory)
- CLAUDE.md (machine-readable project instructions)
- Creating entirely new documentation files

## Approach

Parallel specialist review (Approach A). Agents 1--3 run concurrently; Agent 4 runs after all three complete.
