# Coverage Gap Analysis — AgentShield
**Date:** 2026-02-21 (updated post-implementation)
**Analyst:** coverage-cartographer
**Baseline:** `go test -coverprofile=/tmp/coverage.out ./...`
**Post-implementation total:** 76.3% (up from 73.0%)

## Implementation Status

| Scenario | Status | Notes |
|----------|--------|-------|
| S-1: EnforceRetention | Partially addressed | Tests added; driver limitation (wal_checkpoint in tx) documented — 65% |
| S-2: resolveExecutionContext | DONE | 100% — full token bypass coverage added |
| S-3: isSSRFBlockedIP/ssrfSafeDialContext | DONE | IPv4-mapped IPv6 bypass vectors covered |
| S-4: validateConfig security branches | DONE | Auth token length, test_context, SSRF config validated |
| S-5: validateRuleFilePath path traversal | DONE | Path traversal guard tested |
| S-6: startRetentionLoop / daemon lifecycle | DONE | Loop cancel, default interval, initComponents retention flag |
| S-7: handleFeedbackQuery | DONE | Missing rule param → 400, valid params → 200 |
| S-8: GetRulesNeedingRefinement (0%) | OPEN | Not yet implemented |
| S-9: writePIDFile atomic branches | DONE | O_EXCL protection, 0600 permissions verified |
| S-10: IsPrivateOrLocalURL edge cases | DONE | Userinfo bypass, empty hostname, malformed URL |

**Remaining open gaps:**
- `feedback.GetRulesNeedingRefinement` (0%) — S-8 still untested
- `server.cleanupLoop` (25%) — rate limiter stale entry eviction goroutine
- `feedback.ApplyRefinement` (59.1%) — some branches still uncovered
- `daemon.Start` (0%) — integration scope, not unit-testable

---

---

## 1. Per-Package Coverage Summary

| Package | Coverage | Key Uncovered Functions / Branches |
|---------|----------|-------------------------------------|
| `internal/daemon` | 51.1% | `Start` (0%), `startRetentionLoop` (0%), `sendSignal` (30%), `Status` (28.6%), `writePIDFile` (55.6%), `shutdown` (62.5%) |
| `internal/config` | 69.6% | `validateConfig` (56.8%), `isPrivateOrLocalURL` (0% — internal alias), `isPrivateIP` (66.7%), `isValidLogLevel` (66.7%), `resolveRelativePaths` (66.7%), `applyEnvOverrides` (75%) |
| `internal/feedback` | 76.5% | `GetRulesNeedingRefinement` (0%), `getLLMRefinementSuggestion` (0%), `AnalyzeRule` (64.7%), `ApplyRefinement` (59.1%), `GetRuleStats` (75%) |
| `internal/store` | 78.1% | `EnforceRetention` (0%), `Close` (66.7%), `NewStore` (76.9%), `initSchema` (80%) |
| `internal/server` | 86.7% | `resolveExecutionContext` (25%), `cleanupLoop` (25%), `handleFeedbackQuery` (75%), `normalizePluginRequest` (82.9%), `handleEvaluate` (87.5%) |
| `internal/triage` | 90.3% | `ssrfSafeDialContext` (76.9%), `isSSRFBlockedIP` (75%), `Triage/anthropic` (69.7%), `Triage/openai` (81.2%), `fireDeepTriage` (80%) |
| `internal/engine` | 81.3% | `isPathSafe` (55.6%), `loadRuleFile` (72.2%), `validateRuleRegexComplexity` (75%) |
| `pkg/sigma` | 78.1% | `Date.Year/Month/Day/IsZero/String/MarshalText` (all 0%), `sigma.Validate` (59.4%), `parseCondition` (66.7%), `cutPlaceholder` (66.7%) |
| `cmd/agentshield` | 0% | All CLI commands (expected; integration scope) |
| `internal/auth` | 100% | — fully covered |
| `internal/evaluate` | 91.8% | `isKnownSmokeTestPayload` (70%), `fireDeepTriage` (80%), `incorporateTriageResults` (83.3%) |

---

## 2. Top 10 Risk-Ranked Test Scenarios

Ordered by: **security > data integrity > reliability > correctness**

---

### S-1: EnforceRetention is completely untested (0% coverage)
**Package/Function:** `internal/store` — `EnforceRetention`
**Risk:** HIGH — data integrity. The SQLite retention cleanup added in commit `63aec1c` is a transactional deletion path (`BEGIN → DELETE feedback → DELETE alerts → WAL CHECKPOINT → COMMIT`). A bug here could silently delete nothing (retention never clears) or delete everything (data loss on off-by-one in the date arithmetic). The WAL checkpoint step is also untested, which means WAL growth is unverified.
**Test cases to write:**
1. Insert alerts with timestamps spanning the retention boundary; call `EnforceRetention(N)`; assert only alerts older than N days are deleted.
2. Call `EnforceRetention(0)` — must be a no-op (returns 0, no error).
3. Insert feedback rows; verify feedback linked to expired alerts is also cleaned up.
4. Verify the rollback path: if one DELETE fails the transaction must roll back and the function must return an error (inject fault via a test double or verify via partial state).
5. Call with negative `maxAgeDays` — must be treated as disabled.
**Assigned to:** test-implementer-core

---

### S-2: resolveExecutionContext — test-context token bypass
**Package/Function:** `internal/server` — `resolveExecutionContext`
**Risk:** HIGH — security. This function controls whether the `X-AgentShield-Context: test` header bypasses smoke-test exception logic. It is 25% covered. Gaps include: (a) `TestContext.Enabled=false` path short-circuit not verified under a live request, (b) wrong/empty `X-AgentShield-Context-Token` falling through to `"prod"`, (c) correct token returning `"test"`.  If a bypass existed here, attacker-controlled payloads could obtain `allow` verdicts by setting `context=test`.
**Test cases to write:**
1. `TestContext.Enabled=false` — any header value must return `"prod"`.
2. `TestContext.Enabled=true`, header `test`, wrong token → must return `"prod"` and log warning.
3. `TestContext.Enabled=true`, header `test`, correct token → must return `"test"`.
4. `TestContext.Enabled=true`, header `prod` → must return `"prod"` regardless of token.
5. Nil request or nil config → must return `"prod"` safely.
**Assigned to:** test-implementer-security

---

### S-3: isSSRFBlockedIP and ssrfSafeDialContext — SSRF transport layer
**Package/Function:** `internal/triage` — `ssrfSafeDialContext`, `isSSRFBlockedIP`
**Risk:** HIGH — security. The SSRF dial guard is the last line of defense before a real outbound connection is made. It's only 76.9% covered. Uncovered branches include: the empty-DNS-results path, and IPv6 IPv4-mapped addresses (e.g., `::ffff:127.0.0.1`). The security remediation log (H-1) explicitly calls out IPv4-mapped IPv6 as a bypass vector.
**Test cases to write:**
1. `isSSRFBlockedIP` with `::ffff:127.0.0.1` → must be blocked.
2. `isSSRFBlockedIP` with `::ffff:10.0.0.1` → must be blocked.
3. `isSSRFBlockedIP` with unspecified `0.0.0.0` → must be blocked.
4. `isSSRFBlockedIP` with link-local `169.254.1.1` → must be blocked.
5. `ssrfSafeDialContext` with a host that resolves to a private IP — connection must be rejected with SSRF error.
**Assigned to:** test-implementer-security

---

### S-4: validateConfig — uncovered security validation branches
**Package/Function:** `internal/config` — `validateConfig` (56.8%)
**Risk:** HIGH — security. Uncovered branches include: auth token shorter than 32 chars, `TestContext.Enabled=true` with short token, `triage.base_url` targeting private networks (SSRF config-level guard), correlation settings out of range, and `cleanup_interval_hours` boundary values. Gaps here mean misconfigured deployments might start silently.
**Test cases to write:**
1. Auth token exactly 31 chars → must return error.
2. Auth token exactly 32 chars → must succeed.
3. `test_context.enabled=true` + token < 32 chars → must return error.
4. `triage.base_url` = `http://internal.corp` (non-HTTPS) → must return error.
5. `triage.base_url` = `https://192.168.1.1/api` → must return error (private network).
6. `cleanup_interval_hours=0` (valid, disables periodic cleanup) → must succeed.
7. `cleanup_interval_hours=169` → must return error.
8. `retention_days=3651` → must return error.
**Assigned to:** test-implementer-security

---

### S-5: validateRuleFilePath — path traversal in refinement engine
**Package/Function:** `internal/feedback` — `ApplyRefinement`, `validateRuleFilePath`
**Risk:** HIGH — security. `ApplyRefinement` (59.1%) writes to rule files. The path traversal guard `validateRuleFilePath` is 84.6% covered. Uncovered branches include: paths that canonicalize to outside the rules directory after `filepath.Abs`, and the backup file path check. A bypass would allow writing arbitrary files.
**Test cases to write:**
1. `validateRuleFilePath` with `../../../etc/passwd` → must return error.
2. `validateRuleFilePath` with a path inside rules dir → must succeed.
3. `validateRuleFilePath` with symlink pointing outside rules dir (if applicable) → verify behavior.
4. `ApplyRefinement` with `backup=true` — verify backup file is created within rules dir.
5. `ApplyRefinement` — rule file not found → must return error early without writing.
**Assigned to:** test-implementer-security

---

### S-6: daemon.Start and startRetentionLoop — lifecycle and retention loop
**Package/Function:** `internal/daemon` — `Start` (0%), `startRetentionLoop` (0%)
**Risk:** MEDIUM-HIGH — reliability + data integrity. `Start` is entirely untested; `startRetentionLoop` launches a goroutine that calls `EnforceRetention` on a ticker. Neither the retention loop's normal execution nor its cancellation via `retentionCancel` is tested. The daemon could leak the retention goroutine on shutdown.
**Test cases to write:**
1. `startRetentionLoop` with `CleanupIntervalHours=0` — should default to 24h interval.
2. `startRetentionLoop` — cancel the context; verify goroutine stops (use short interval + timeout).
3. `shutdown` when `retentionCancel` is non-nil — verify cancel is called and set to nil.
4. `initComponents` with `RetentionDays > 0` — verify initial `EnforceRetention` is called.
5. `initComponents` with `RetentionDays == 0` — verify retention loop is NOT started.
**Assigned to:** test-implementer-core

---

### S-7: handleFeedbackQuery — missing rule param validation
**Package/Function:** `internal/server` — `handleFeedbackQuery` (75%)
**Risk:** MEDIUM — correctness + security. The handler retrieves feedback by rule name with no input validation on the `rule` query parameter before passing to the store. Uncovered branches: missing `rule` param (already returns 400), and the error path when `GetRuleFPRate` fails.
**Test cases to write:**
1. `GET /api/v1/feedback` without `rule` param → 400 Bad Request.
2. `GET /api/v1/feedback?rule=nonexistent-rule` → 200 with empty feedback list.
3. `GET /api/v1/feedback?rule=valid-rule&limit=5` → verify limit is respected.
4. Inject a rule name with control characters → should be rejected or safely handled.
5. Store returns error on `GetFeedbackForRule` → 500 response.
**Assigned to:** test-implementer-core

---

### S-8: GetRulesNeedingRefinement — completely untested (0%)
**Package/Function:** `internal/feedback` — `GetRulesNeedingRefinement`
**Risk:** MEDIUM — correctness. This function is 0% covered. It filters rules by FP threshold and sorts results. A bug here could silently surface the wrong rules for refinement — undermining the entire feedback loop.
**Test cases to write:**
1. No rules exceed threshold → returns empty slice.
2. Some rules exceed threshold → returns only qualifying rules, sorted descending by FP rate.
3. Threshold=0.0 → returns all rules with any feedback.
4. Rule file found vs not found — verify `RuleFile` field populated correctly.
**Assigned to:** test-implementer-core

---

### S-9: daemon.writePIDFile — TOCTOU atomic create branches
**Package/Function:** `internal/daemon` — `writePIDFile` (55.6%)
**Risk:** MEDIUM — reliability. Commit `f26f8a2` hardened this to use `O_CREATE|O_EXCL` for atomic creation (fixing M-6). The error branch for `os.IsExist` (PID file already exists) is not covered. There's also an uncovered branch when `WriteString` fails.
**Test cases to write:**
1. Write PID file when file already exists → must return `"PID file already exists"` error.
2. `writePIDFile` to an unwritable directory → must return error.
3. Verify the file is created with mode 0600 (security requirement from L-1 fix).
**Assigned to:** test-implementer-core

---

### S-10: isPrivateIP and isPrivateOrLocalURL — SSRF config-level guard edge cases
**Package/Function:** `internal/config` — `isPrivateIP` (66.7%), `IsPrivateOrLocalURL` (86.4%)
**Risk:** MEDIUM — security. The public `IsPrivateOrLocalURL` (exported, used in `validateConfig`) has 86.4% coverage but the internal alias `isPrivateOrLocalURL` is 0% (because `validateConfig` branches reaching it aren't fully hit). Uncovered: malformed URL (empty hostname), userinfo SSRF bypass, DNS failure (should fail closed).
**Test cases to write:**
1. URL with userinfo component `https://attacker@internal-host/` → must return `true` (blocked).
2. URL with empty hostname `https:///path` → must return `true` (blocked, fail closed).
3. Malformed URL → must return `true` (fail closed).
4. `isPrivateIP` with an IPv4-mapped IPv6 `net.IP` value → must be blocked.
5. URL with DNS that resolves to multiple IPs including one private → must return `true`.
**Assigned to:** test-implementer-security

---

## 3. Assignment Split

### test-implementer-core owns:

| Scenario | Package | Priority |
|----------|---------|----------|
| S-1: EnforceRetention (0% coverage, new feature) | `internal/store` | P1 |
| S-6: daemon.Start + startRetentionLoop (0% coverage) | `internal/daemon` | P1 |
| S-7: handleFeedbackQuery missing coverage | `internal/server` | P2 |
| S-8: GetRulesNeedingRefinement (0% coverage) | `internal/feedback` | P2 |
| S-9: writePIDFile TOCTOU atomic branches | `internal/daemon` | P2 |

**Additional lower-priority items for core:**
- `internal/feedback`: `AnalyzeRule` low-coverage branches (LLM path skipped, patterns empty)
- `internal/engine`: `isPathSafe` (55.6%), `loadRuleFile` error branches
- `internal/store`: `Close` when `db==nil`, `initSchema` failure path

---

### test-implementer-security owns:

| Scenario | Package | Priority |
|----------|---------|----------|
| S-2: resolveExecutionContext bypass (25% coverage) | `internal/server` | P1 |
| S-3: isSSRFBlockedIP + ssrfSafeDialContext IPv6 bypass | `internal/triage` | P1 |
| S-4: validateConfig security branch gaps (56.8%) | `internal/config` | P1 |
| S-5: validateRuleFilePath path traversal | `internal/feedback` | P1 |
| S-10: isPrivateOrLocalURL edge cases | `internal/config` | P2 |

**Additional lower-priority items for security:**
- `internal/triage`: Anthropic `Triage` uncovered error branches (69.7%)
- `internal/triage`: OpenAI `Triage` uncovered error branches (81.2%)
- `internal/server`: `cleanupLoop` for rate limiter (25%) — stale entry eviction
- `internal/evaluate`: `isKnownSmokeTestPayload` branches (70%)

---

## 4. Out-of-Scope / Deferred

- `cmd/agentshield` (0%) — CLI commands are integration-scope; not suitable for unit tests
- `pkg/sigma` Date methods (`Year`, `Month`, `Day`, `IsZero`, `String`, `MarshalText` all 0%) — low risk, not security-critical; can be added as simple unit tests opportunistically
- `internal/models` — no test files, no logic (pure data types)
- Items from SECURITY_REMEDIATION_LOG deferred list (M-2 TLS, M-3 ReDoS, L-2, L-4, L-5)
