# Security Remediation Log — 2026-02-20

## Context
Continuing from commit `7fbb50e` which addressed C-1 (triage downgrade), C-2 (hook auth), C-3 (client mode override), C-4 (constant-time compare).

This log tracks remediation of remaining HIGH and MEDIUM findings from `REVIEW_CRITICAL_2026-02-20.md`.

---

## Batch 1 — Trivial/Low-effort fixes (H-5, H-6, M-1, L-1)
**Timestamp:** 2026-02-20T18:00Z

### Changes
| ID  | Finding | Fix |
|-----|---------|-----|
| H-5 | Verbose config logging leaks secrets | Replaced `%+v` dump with selective field logging (addr, mode, rules dir, db path, auth enabled, triage enabled) in `cmd/agentshield/main.go` |
| H-6 | ToolName/Command fields bypass validation | Added `validateStringInput` calls for `ToolName` (max 100) and `Command` (max `MaxFieldValueLength`) in `validateEvaluationRequest()` in `internal/server/server.go` |
| M-1 | sanitizeComment is a no-op (stored XSS) | Implemented HTML entity encoding (`<` → `&lt;`, `>` → `&gt;`) in `internal/feedback/feedback.go` |
| L-1 | PID file world-readable (0644) | Changed to 0600 in `internal/daemon/daemon.go` |

### Tests
- `TestToolNameCommandValidation` (H-6) — overlong/valid tool_name and command
- `TestSanitizeCommentXSS` (M-1) — script, iframe, img, plain text, empty string
- Existing daemon tests cover L-1 PID file creation

### Tradeoffs
- M-1 uses simple entity encoding rather than a full HTML sanitizer library; sufficient for the JSON API context where output is not rendered as HTML.

---

## Batch 2 — SSRF, health leak, headers, validation order (H-1, H-3, M-5, M-8)
**Timestamp:** 2026-02-20T18:15Z

### Changes
| ID  | Finding | Fix |
|-----|---------|-----|
| H-1 | SSRF bypass via hex IPs, IPv6, DNS rebinding | Rewrote `isPrivateOrLocalURL` in `internal/config/config.go` to parse URLs, resolve DNS, and check all resolved IPs against private ranges. Added `ssrfSafeDialContext` in `internal/triage/triage.go` that blocks connections to private/local IPs at the transport level. Added `createLocalHTTPClient` for intentionally-local services (OpenClaw gateway, deep triage). |
| H-3 | Health endpoint leaks evaluation_mode, rules_dir, auth_enabled | Stripped config map to empty `map[string]string{}` in health response (`internal/server/server.go`) |
| M-5 | Missing security response headers | Added `securityHeaders` middleware: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Cache-Control: no-store`, `Content-Security-Policy: default-src 'none'` |
| M-8 | RawParams bypass validation (normalize after validate) | Reordered `normalizePluginRequest()` before `validateEvaluationRequest()` in `handleEvaluate()` |

### Tests
- `TestSSRFBypassVectors` (H-1) — 12 vectors: localhost, 127.0.0.1, ::1, 10.x, 192.168.x, 172.16.x, 0.0.0.0, ip6-localhost, malformed URL, empty hostname, public IP, public domain
- `TestHealthEndpointNoConfigLeak` (H-3) — verifies evaluation_mode, rules_dir, auth_enabled, store_healthy absent
- `TestSecurityHeadersPresent` (M-5) — checks all four headers
- `TestRawParamsValidationBypass` (M-8) — overlong value via params.command rejected

### Tradeoffs
- H-1: DNS resolution in SSRF check adds latency on first request; acceptable for security-critical triage calls.
- H-3: Operators lose health-page visibility into config; use `/api/v1/config` (authenticated) for diagnostics.
- Existing triage tests updated to use `createLocalHTTPClient` for httptest servers that intentionally connect to localhost.

---

## Batch 3 — Deep triage, rate limit, openclaw, PID, sqlite, dead code (H-2, H-4, M-4, M-6, M-7, L-3)
**Timestamp:** 2026-02-20T18:30Z

### Changes
| ID  | Finding | Fix |
|-----|---------|-----|
| H-2 | Deep triage tool_use allows web_fetch (SSRF/data exfil) | Added `filterSafeTools()` in `internal/triage/deep.go` that excludes `web_fetch` unless operator explicitly configured it. Added `[DATA BEGIN]`/`[DATA END]` delimiters around user-controlled data and prompt injection warning in triage prompts. |
| H-4 | No rate limiting on /evaluate endpoint | Added `ipRateLimiter` with per-IP token bucket (100 req/min, burst 20) and cleanup goroutine in `internal/server/server.go`. Applied as middleware in `Start()`. |
| M-4 | OpenClaw timeout_ms=50 too tight, timeout_policy="allow" unsafe | Changed defaults to `timeout_ms: 200`, `timeout_policy: "block"` in `plugins/openclaw/src/config.ts` |
| M-6 | PID file TOCTOU race (stat then write) | Rewrote `writePIDFile()` to use `os.OpenFile` with `O_CREATE|O_EXCL` for atomic creation in `internal/daemon/daemon.go` |
| M-7 | SQLite MaxOpenConns=25 causes SQLITE_BUSY | Changed to `MaxOpenConns=1`, `MaxIdleConns=1` in `internal/store/store.go` (serializes writes) |
| L-3 | Dead handleFeedback() method still in codebase | Removed from `internal/server/server.go` and corresponding test from `server_test.go` |

### Tests
- `TestTriageCannotDowngradeBlockEnforce` (C-1 regression via triage mock)
- `TestClientModeOverrideRejected` (C-3 regression)
- `TestTriageEscalateAllowToBlock` (triage escalation direction)
- `TestTriageCannotDowngradeBlock` (E2E via server)
- All existing triage, server, store, daemon tests pass

### Tradeoffs
- H-4: Rate limit is per-IP; behind a shared proxy all clients share the same bucket. Document in ops guide.
- M-7: Single SQLite writer limits throughput; acceptable for the expected request volume. Consider WAL mode or PostgreSQL for higher scale.
- H-2: Removing web_fetch from deep triage defaults reduces investigation capability; operators can re-enable via config if they accept the risk.

---

## Batch 4 — Security test suite
**Timestamp:** 2026-02-20T18:45Z

### New test files
| File | Tests |
|------|-------|
| `internal/config/security_test.go` | `TestSSRFBypassVectors` (H-1) |
| `internal/evaluate/security_test.go` | `TestTriageCannotDowngradeBlockEnforce` (C-1), `TestClientModeOverrideRejected` (C-3), `TestTriageEscalateAllowToBlock` |
| `internal/feedback/security_test.go` | `TestSanitizeCommentXSS` (M-1) |
| `internal/server/security_test.go` | `TestToolNameCommandValidation` (H-6), `TestRawParamsValidationBypass` (M-8), `TestHealthEndpointNoConfigLeak` (H-3), `TestSecurityHeadersPresent` (M-5), `TestTriageCannotDowngradeBlock` (C-1 regression) |

### Updated test files
- `internal/server/server_test.go` — removed `TestHandleFeedback` (dead code L-3), updated health tests (H-3)
- `internal/triage/triage_test.go` — switched all test providers to `createLocalHTTPClient` to avoid SSRF false positives on httptest localhost servers

### Result
```
ok  github.com/agentshield-ai/agentshield/internal/auth
ok  github.com/agentshield-ai/agentshield/internal/config
ok  github.com/agentshield-ai/agentshield/internal/daemon
ok  github.com/agentshield-ai/agentshield/internal/engine
ok  github.com/agentshield-ai/agentshield/internal/evaluate
ok  github.com/agentshield-ai/agentshield/internal/feedback
ok  github.com/agentshield-ai/agentshield/internal/server
ok  github.com/agentshield-ai/agentshield/internal/store
ok  github.com/agentshield-ai/agentshield/internal/triage
ok  github.com/agentshield-ai/agentshield/pkg/sigma
```

---

## Remaining Items

| ID  | Severity | Finding | Status | Notes |
|-----|----------|---------|--------|-------|
| M-2 | MEDIUM | No TLS support; relies on reverse proxy | Deferred | Requires TLS cert management; recommend nginx/caddy in front |
| M-3 | MEDIUM | ReDoS potential in rule patterns | Deferred | Requires regex audit across all Sigma rules; low practical risk with current rule set |
| L-2 | LOW | Health checks consume API credits | Deferred | Can be mitigated by increasing health check interval; low priority |
| L-4 | LOW | Hardcoded false-positive rates | Deferred | Requires telemetry data to calibrate; cosmetic impact only |
| L-5 | LOW | Error responses may leak upstream details | Deferred | Current error wrapping is minimal; audit error paths in next review cycle |

## Next Actions
1. **M-2**: Add TLS listener option (`--tls-cert`, `--tls-key` flags) or document reverse proxy requirement in ops guide.
2. **M-3**: Run `rg` audit of all Sigma rule regex patterns for catastrophic backtracking; consider RE2 engine.
3. **L-2**: Make health check a lightweight `/models` list call instead of a full completion request.
4. **L-4**: Add configurable FP/FN rate overrides in `config.yaml`.
5. **L-5**: Wrap all upstream errors with generic messages before returning to clients.
6. Schedule follow-up security review after items above are addressed.
