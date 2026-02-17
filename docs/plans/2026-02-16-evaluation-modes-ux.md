# Evaluation Modes & UX Improvements Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add three evaluation modes (enforce/audit/shadow), structured block responses with feedback hooks, and inline feedback endpoint — all server-side in AgentShield, with zero changes to OpenClaw.

**Architecture:** Extend the existing realtime evaluation pipeline with a `mode` field that controls action decisions. The `EvaluationResponse` grows richer fields (overridable, feedback_url, effective_mode). A new `/api/v1/feedback` endpoint accepts feedback. The existing OpenClaw plugin works unchanged — it only reads `action` and `reason`, so extra response fields are silently ignored. Mode is controlled entirely via AgentShield's `config.yaml`.

**Tech Stack:** Python 3.11+ (aiohttp, Pydantic v2), pytest, SQLite (aiosqlite)

---

## Background

Currently, AgentShield has a single operating mode: alerts above `block_threshold` severity always block, everything else logs. Users have no way to:

1. Run in "audit-only" mode (log everything, block nothing)
2. Override a block when it's a false positive
3. Submit feedback when a rule fires
4. See which mode the server is running in

This plan implements all four capabilities purely server-side. The existing OpenClaw plugin requires no modifications because:

- **Mode control is server-side** — set `evaluation_mode: audit` in config.yaml and the server returns `"log"` instead of `"block"`. The plugin already handles `"log"` responses.
- **Extra response fields are ignored** — the plugin only reads `response.action`, `response.reason`, and `response.alerts`. New fields (`overridable`, `feedback_url`, `effective_mode`) are returned but silently ignored by any unmodified client.
- **Feedback goes through the API** — `POST /api/v1/feedback` can be called by curl, the CLI, the MCP server, or any future client.
- **`mode_override` per-request** — available in the API for any client that wants it, but not required.

### Key Terminology

- **enforce** — Current behaviour. Blocks at or above threshold, logs below.
- **audit** — Logs everything including what would have been blocked. Never blocks. ("yolo mode")
- **shadow** — Evaluates silently. No actions returned, no blocks. Useful for rule testing.
- **overridable** — A block that the agent/user can choose to proceed past (severity-gated).

---

## Task 1: Add EvaluationMode Enum and Config [DONE]

**Commit:** `96a2182`

**Files:**
- Modify: `src/agentshield/realtime/models.py`
- Modify: `src/agentshield/config.py`
- Test: `tests/test_realtime_models.py`
- Create: `tests/test_config_evaluation_mode.py`

Added `EvaluationMode(StrEnum)` with ENFORCE/AUDIT/SHADOW values.
Added `evaluation_mode: str = "enforce"` to `RealtimeConfig`.
8 tests (5 enum + 3 config).

---

## Task 2: Mode-Aware Action Decision Logic [DONE]

**Commit:** `22f8049`

**Files:**
- Modify: `src/agentshield/realtime/action.py`
- Test: `tests/test_realtime_action.py`

Added `mode` parameter to `decide_action()`:
- AUDIT mode: never blocks, returns `"log"` for any alerts.
- SHADOW mode: always returns `"allow"`.
- ENFORCE mode: existing behaviour (blocks at/above threshold).
- Default is ENFORCE when mode is None (backwards compatible).

8 new tests in `TestDecideActionWithMode`.

---

## Task 3: Enrich EvaluationRequest and EvaluationResponse Models [DONE]

**Commit:** `d77ad34`

**Files:**
- Modify: `src/agentshield/realtime/models.py`
- Test: `tests/test_realtime_models.py`

Added to `EvaluationRequest`:
- `mode_override: str | None = None` — client can request audit/shadow

Added to `EvaluationResponse`:
- `overridable: bool = False` — whether block can be overridden
- `feedback_url: str | None = None` — URL for feedback submission
- `suggested_alternative: str | None = None` — safer alternative
- `effective_mode: str | None = None` — mode actually used

All fields have defaults, so existing clients are unaffected. 5 new tests.

---

## Task 4: Wire Mode Into Handler and Build Enriched Response [DONE]

**Commit:** `311399f`

**Files:**
- Modify: `src/agentshield/realtime/action.py` (add `is_overridable`)
- Modify: `src/agentshield/realtime/handlers.py`
- Modify: `src/agentshield/realtime/server.py`
- Test: `tests/test_realtime_action.py`
- Test: `tests/test_realtime_integration.py`

Added `is_overridable()` — critical blocks are not overridable, everything else is.
Added `evaluation_mode` parameter to `RealtimeHandlers.__init__` and `RealtimeServer.__init__`.
Added `_resolve_mode()` — client can downgrade (enforce→audit→shadow), never upgrade.
Updated `handle_evaluate` to use effective mode, populate enriched response fields.

9 new tests (5 overridable + 4 integration).

---

## Task 5: Add Inline Feedback Endpoint [DONE]

**Commit:** `6636871`

**Files:**
- Modify: `src/agentshield/realtime/models.py` (add FeedbackRequest/Response)
- Create: `src/agentshield/realtime/feedback_handler.py`
- Modify: `src/agentshield/realtime/server.py` (register route)
- Create: `tests/test_realtime_feedback.py`

New `POST /api/v1/feedback` endpoint:
- Accepts `event_id`, `rule_id`, `feedback_type` ("safe"/"threat"), optional `comment`
- Stores via existing `FeedbackStore` for the refinement engine
- Returns 201 on success, 400 on bad input, 500 on storage error

7 new tests (4 model + 3 integration).

---

## Task 6: Health Endpoint Reports Mode [DONE]

**Commit:** `31b8594`

**Files:**
- Modify: `src/agentshield/realtime/models.py`
- Modify: `src/agentshield/realtime/handlers.py`
- Test: `tests/test_realtime_models.py`

Added `evaluation_mode: str = "enforce"` to `HealthResponse`.
Updated `handle_health` to populate from `self.evaluation_mode.value`.
2 new tests.

---

## Task 7: Full Integration Verification [DONE]

**Results:**
- **631 tests pass** (0 failures)
- **ruff clean** on all changed files
- **pyright clean** on realtime module (0 errors, 0 warnings)

### Mode resolution matrix (verified in integration tests):

| Server mode | Client override | Effective mode |
|------------|----------------|----------------|
| enforce | (none) | enforce |
| enforce | audit | audit |
| enforce | shadow | shadow |
| audit | (none) | audit |
| audit | enforce | audit (can't upgrade) |
| audit | shadow | shadow |
| shadow | (none) | shadow |
| shadow | enforce | shadow (can't upgrade) |
| shadow | audit | shadow (can't upgrade) |

---

## Summary of Changes

### AgentShield Python (6 files modified, 2 created)

| File | Change |
|------|--------|
| `src/agentshield/realtime/models.py` | `EvaluationMode` enum, `mode_override` on request, `overridable`/`feedback_url`/`suggested_alternative`/`effective_mode` on response, `FeedbackRequest`/`FeedbackResponse`, `evaluation_mode` on health |
| `src/agentshield/realtime/action.py` | `mode` param on `decide_action()`, new `is_overridable()` |
| `src/agentshield/realtime/handlers.py` | `evaluation_mode` param, `_resolve_mode()`, enriched response construction |
| `src/agentshield/realtime/server.py` | Pass `evaluation_mode` and `feedback_store` through |
| `src/agentshield/realtime/feedback_handler.py` | **New** — `FeedbackHandler` for `POST /api/v1/feedback` |
| `src/agentshield/config.py` | `evaluation_mode` field on `RealtimeConfig` |

### OpenClaw TypeScript — NO CHANGES

The existing plugin works unchanged. Extra response fields are silently ignored by JSON parsing. Mode is controlled server-side via `config.yaml`.

### New Tests (2 files created, 3 files extended)

| File | Tests |
|------|-------|
| `tests/test_config_evaluation_mode.py` | 3 tests |
| `tests/test_realtime_feedback.py` | 7 tests (4 model + 3 integration) |
| `tests/test_realtime_models.py` | +7 tests (enum, mode_override, enriched response, health mode) |
| `tests/test_realtime_action.py` | +13 tests (mode-aware action + overridable) |
| `tests/test_realtime_integration.py` | +4 tests (mode handling) |

### How to Use

**Switch to audit mode (yolo — log only, never block):**
```yaml
# ~/.agentshield/config.yaml
realtime:
  evaluation_mode: audit
```

**Switch to shadow mode (silent evaluation, no actions):**
```yaml
realtime:
  evaluation_mode: shadow
```

**Submit feedback on a fired rule:**
```bash
curl -X POST http://127.0.0.1:8432/api/v1/feedback \
  -H "Content-Type: application/json" \
  -H "X-AgentShield-Auth: $TOKEN" \
  -d '{"event_id": "evt-123", "rule_id": "rule-456", "feedback_type": "safe", "comment": "False positive"}'
```

**Check current mode via health endpoint:**
```bash
curl http://127.0.0.1:8432/api/v1/health
# {"status": "ok", "version": "1.0.0", "rules_loaded": 36, "evaluation_mode": "enforce", ...}
```

### Future OpenClaw Enhancement (optional, not required)

If desired in a future iteration, the OpenClaw plugin could be updated to:
- Send `mode_override` in evaluation requests (per-request mode downgrade)
- Display `overridable` and `suggested_alternative` in block reasons
- Call `/api/v1/feedback` inline after blocks

These are additive enhancements that can be done independently when the plugin is next modified.
