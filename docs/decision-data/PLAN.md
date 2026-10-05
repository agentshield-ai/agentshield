# Decision data: plan

This plan covers capturing AgentShield's decisions, labelling their outcomes, evaluating decision makers against those outcomes, and choosing actions from calibrated probabilities. It describes where decisions are made today, what each decision point records, the packages to be built in dependency order, and the files that will change. Nothing in this plan changes what AgentShield blocks. Points that need a decision before or during implementation are collected in the final section.

The design note `claude/telemetry-to-action-moat.md` was not available in this environment, so the plan is based on the task description and the repository alone.

## 1. Where decisions are made today

### 1.1 Pre-execution: `POST /api/v1/evaluate`

The route is also served at the legacy path `/evaluate`. The flow is:

1. `server.handleEvaluate` (`internal/server/server.go:595`) decodes an `models.EvaluationRequest`.
2. `normalizePluginRequest` folds the plugin aliases (`tool_name`, `command`, `params`) into `Tool`, `Args` and a flat `Fields` map.
3. The request is validated.
4. `evaluate.Evaluator.EvaluateWithContext` (`internal/evaluate/evaluate.go:117`) does the following in order:
   - injects session-derived and cross-session fields from `session.Registry` when session tracking is on;
   - checks the verdict cache, and on a hit returns the cached action and alerts with `cached: true`;
   - runs the Sigma rules (`engine.Evaluate`);
   - runs fast triage if it is enabled and any rule matched;
   - sets the action from severity counts and the configured mode in `determineAction`;
   - writes the result to the cache;
   - records metrics;
   - appends the event to the session window;
   - starts deep triage asynchronously.

Triage results are returned for observability but never change the action. That invariant is stated at `evaluate.go:256`.

**What is recorded.** `storeMatchedAlerts` writes one row per matched rule to the SQLite `alerts` table. Each row holds the rule ID and name, severity, tool, the full argument map as unredacted JSON, the action, the timestamp, the session ID and the event ID. Nothing else is recorded:

- When no rule matches, nothing is written, so allowed actions leave no trace.
- Fast triage results, the effective mode, the cache-hit flag, the session-derived fields and the full field map are not stored.
- Several fields that plugins send are dropped silently by the JSON decoder because `EvaluationRequest` has no field for them: `working_dir`, `agent_id`, `timestamp` and `data`.
- Deep triage results go only to the log and an optional webhook.

**Which plugins call it.** Every plugin calls this endpoint before a tool runs. Claude Code, Codex and Gemini also call it for each submitted user prompt, with `event_type: user_input`, `tool_name: user_prompt` and the prompt in `params.message`. Those plugins treat the verdict on a prompt as detection-only and never block the prompt. So a call to `/evaluate` is not always blockable. The record has to distinguish the two cases instead of inferring blockability from the endpoint.

**How plugins handle `require_approval`.** Every plugin currently treats `require_approval` as a block and never asks the user:

- Claude Code: `plugins/claude/agentshield-hook.sh:187`.
- Gemini: `plugins/gemini/.../hook.py:329`.
- Hermes, OpenClaw and the MCP gateway behave the same way.
- Codex has an opt-in mode that hands the decision to Codex's own prompt. However, its `PermissionRequest` hook re-evaluates the call and denies it, probably before the user is asked.

None of the plugins learns what the user would have chosen, and none calls `/override` or `/feedback`. This matters for WP3 (see section 4, decision D1).

### 1.2 Post-execution: `POST /api/v1/audit`

`server.handleAuditEvent` (`server.go:881`) decodes the payload as an untyped map. It then calls `scanAuditEventForInjection`, which uses `toolresult.DetectionFields` to map a `tool_result` payload's `result_summary` and `error_message` onto the rule vocabulary, and calls `Evaluator.MatchRules`. That pass has no session context, cache, triage or mode logic. The handler always returns 202, because the tool has already run and this endpoint can only detect.

**What is recorded.** Only matched rules are stored, as `alerts` rows with action `log` and empty args. Everything else is discarded:

- payloads with no match;
- non-tool-result payloads;
- the `correlation_id` that links the result back to the pre-execution `event_id`;
- `is_error`, `duration_ms` and `working_dir`.

Plugins cap `result_summary` at 1,000 characters and do not resend the tool arguments.

**Correlation.** Plugins set `correlation_id` to the `event_id` of the matching `/evaluate` call. They find it by first-in-first-out matching per session and tool. If no match is found, they fall back to a fresh UUID. The link is therefore best-effort and the record must say so.

### 1.3 Related endpoints that carry outcome signals

- **`POST /api/v1/lifecycle`** (`server.go:942`). The payload is checked for valid JSON and discarded. Plugins send `session_start` and `session_end` events, and OpenClaw also sends `agent_start` and `agent_end`. Their content varies:
  - Gemini includes an end `reason`.
  - OpenClaw sends `success`, `error` and `duration_ms` on `agent_end`.
  - Claude Code sends `session_end` on every `Stop` event, which fires after every turn. So for that plugin, `session_end` does not mean the session ended.
- **`POST /api/v1/override`** (`server.go:984`). This sets an in-memory flag in the session registry and is not persisted. It returns 503 when session tracking is off. No plugin calls it, so `session.override_count` is always zero in practice.
- **`POST /api/v1/feedback`** (`server.go:787`). This stores `false_positive`, `true_positive` or `false_negative` in the SQLite `feedback` table, keyed by event and alert. Feedback is phrased in terms of rule verdicts, not outcomes. No plugin calls it.

### 1.4 Existing redaction and storage

**Redaction.** The only redaction in the codebase is `triage.maskSensitiveData` (`internal/triage/triage.go:223`). It masks an argument when its key name contains a word such as `token` or `password`. It does not inspect values, so a secret inside a shell command passes through unchanged.

**Storage.** SQLite (`modernc.org/sqlite`) holds alerts and feedback unencrypted, with an age-based retention loop in `daemon.startRetentionLoop`.

**Dependencies.** The module has no third-party cryptography dependency. The standard library's `crypto/aes`, `crypto/cipher` and `crypto/rand` are sufficient for what follows.

**Rule set identity.** The engine does not expose an identifier for the loaded rule set. A decision record needs one, so that records made under different rule versions can be told apart.

## 2. Principles applied throughout

- **Off by default.** Capture is controlled by a new `decision_data.enabled` setting, which defaults to false. When it is off, no new code runs on any request path beyond a nil check.
- **No egress.** No new code opens a network connection. Records, labels and keys stay under a local directory. Nothing in this plan adds an export or upload path. If one is wanted later, it will need its own explicit setting and your approval first.
  - Fast triage, when an operator enables it, already sends alert context to a hosted LLM provider. That path is unchanged. Each record notes which triage provider produced its triage result, so hosted and local triage can be told apart.
- **Redact before disk.** Redaction runs in memory before a record is serialised. No unredacted record is ever written, including to temporary files or logs. Redaction is best-effort, so records are also encrypted at rest.
- **Encrypt at rest.** Each record is sealed with AES-256-GCM before it is written.
- **Shadow only.** No learned component, and no new action-selection code, feeds `determineAction` or the HTTP response. Tests assert that the `/evaluate` response is byte-for-byte identical with capture and shadow scoring on or off.
  - If you later approve a learned component to act, it will be combined as the stricter of the rule verdict and the model's choice, under the ordering allow < log < require_approval < block. It will never be able to weaken a rule verdict.
- **Capture never blocks the decision.** Records are built cheaply on the request goroutine and handed to a bounded queue. A background writer redacts, encrypts and writes them. If the queue is full, the record is dropped and counted. The request is never delayed or failed.

## 3. Work packages, in dependency order

Each package lists the code it adds, the files it changes, and what it measures. At the end of each package a report goes to `reviews/decision-data/` (for example `reviews/decision-data/WP1-schema.md`).

### WP1: Decision record schema

**New packages.**

- **`internal/decisionrecord`** holds the Go types for schema version 1:
  - a `SchemaVersion` constant;
  - `Encode` and `Decode` functions that dispatch on the stored version;
  - an upgrade function per past version, so old records stay readable. Decoding tolerates unknown fields.
- **`internal/redact`** is built here, although its tests belong to WP2, because the record builder depends on it. It provides value-based redaction with typed placeholders such as `[REDACTED:github_token]`. It covers:
  - well-known token formats (cloud provider keys, source-host tokens, chat-platform tokens, LLM provider keys, JWTs, PEM private-key blocks);
  - `Authorization` headers and bearer and basic credentials;
  - credentials embedded in URLs;
  - `KEY=value` assignments and command-line flags whose names suggest a secret;
  - a configurable high-entropy fallback;
  - personal data: email addresses, and user names in home-directory paths (`/home/alice/...` becomes `/home/<user>/...`).

**The record (schema version 1).** Every field and the reason it exists will be documented in `docs/decision-data/schema.md`. In summary:

- **`schema_version`**: lets readers decode old records.
- **`record_id`, `created_at`**: identity and ordering.
- **`origin`**: either `live` (from the engine) or `replay` (built from a public trace by WP4), so the two are never mixed by accident.
- **`decision_point`**: one of three values, each paired with an `enforceable` flag.

  | Value | Source | `enforceable` |
  |---|---|---|
  | `pre_execution_tool_call` | `/evaluate` | true |
  | `user_prompt` | `/evaluate`, detection-only by plugin convention | false |
  | `post_execution_tool_result` | `/audit` | false |

- **`session`**:
  - the session ID;
  - the position in the session (a per-session sequence number, plus an engine start epoch, because the counter resets on restart);
  - the source plugin and agent ID, where sent.
- **`task`**: the most recent user prompt seen in the session, redacted and truncated, with the record ID of the prompt event it came from. It is empty when the plugin sends no prompts, which is the case for Hermes, OpenClaw and the MCP gateway.
- **`history`**: up to N preceding steps in the session (N is configurable, default 20). Each step is redacted and truncated, and holds:
  - the decision point;
  - the tool;
  - the command, file path or URL;
  - the action;
  - the matched rule IDs;
  - the timestamp.
- **`action_under_review`**:
  - the tool and canonical event type;
  - the redacted argument map;
  - the redacted field map that the rules saw;
  - for post-execution records, the redacted output text, `is_error`, and the `correlation_id` with a flag saying whether it matched a captured pre-execution record.
- **`host_context`**: context that AgentShield already has:
  - the working directory, redacted;
  - the execution context (`prod` or `test`);
  - the effective mode;
  - whether the verdict was served from the cache;
  - the session-derived and cross-session fields, if session tracking is on;
  - the engine version;
  - the rule set fingerprint (see WP2).
- **`verdicts`**: everything the current system produced:
  - each matched rule (ID, name, severity), without the duplicated `MatchedFields` map;
  - each fast triage result (verdict, confidence, provider, model), with the reasoning text redacted;
  - the action AgentShield returned and whether it was overridable.
- **`reference_labels`**: labels derived mechanically from `verdicts`, such as "rules said block" or "triage said allow". They are kept separate so that a model is never scored against them as if they were truth.
- **`outcome_labels`**: empty at capture time and filled by WP3.

### WP2: Capture

**New packages.**

- **`internal/decisionstore`** is an append-only, rotating, encrypted store.
  - **Location.** It lives in a directory (default `~/.agentshield/decision-data/`, mode 0700) with `records/` and `labels/` subdirectories.
  - **Segment files.** Each segment file has a header holding a magic value, format version and key ID. The header is followed by length-prefixed frames. Each frame is one record, sealed with AES-256-GCM under a fresh random nonce. The segment ID and frame index are used as additional data, so that frames cannot be reordered or moved between files undetected.
  - **Rotation.** Segments rotate on size or age.
  - **Retention.** This deletes whole segments: those older than `retention_days`, and the oldest ones once the total exceeds `max_total_mb`.
  - **Append-only.** Records are never rewritten. Labels are appended as separate entries that refer to a record ID.
  - **Key.** The key is 32 random bytes, generated on first use and stored in a 0600 file in the same directory. A `key_file` setting can point elsewhere. See decision D2.
- **`internal/capture`** contains the record builder, the per-session history ring and the bounded queue with its background writer.
  - **History ring.** It is bounded by session count and TTL, and works whether or not session tracking is enabled.
  - **Builder.** It copies only what it needs. Redaction and encryption happen in the writer goroutine.

**Changed files.**

- `internal/config/config.go`: a `decision_data:` section with these settings: `enabled` (false), `dir`, `key_file`, `retention_days`, `max_total_mb`, `segment_max_mb`, `history_steps`, `max_field_chars`, `queue_size`. It also gets validation, including a check that `dir` is a local path.
- `internal/models/models.go`: optional `WorkingDir`, `AgentID` and `Timestamp` fields on `EvaluationRequest`, so the values plugins already send are kept for the record. These fields are not copied into the rule field map, so rule matching and the cache key are unchanged. A test will assert this.
- `internal/engine/engine.go`: compute a SHA-256 fingerprint of the loaded rule files at load and reload time, and expose it.
- `internal/server/server.go`: call capture from `handleEvaluate` on both the cache-hit and cache-miss paths, and from `handleAuditEvent` for every tool-result payload, matched or not. Lifecycle and override payloads are passed to capture as session events for WP3. Responses and status codes are unchanged.
- `internal/daemon/daemon.go`: build capture when it is enabled, and flush it on shutdown.
- `docs/configuration.md` and `docs/decision-data/capture.md`.

**Tests.**

- **Redaction table tests.** These use realistic examples: shell commands with inline keys, `curl` calls with auth headers, `.env` file contents, git remotes with embedded credentials, private-key blocks inside tool output, JWTs inside JSON, and emails and home paths inside prompts. They also include negative cases (hashes, UUIDs, base64 image fragments), to report how much benign text is redacted.
- **Disk tests.** These scan written segment files for every planted secret and assert that none appear, in either plaintext or base64.
- **Store tests.** These cover rotation, retention, tamper detection, a wrong key, and decoding of older schema versions.

**Latency measurement.**

- **Setup.** A Go benchmark drives `handleEvaluate` through `httptest` with the project rules loaded. It uses a mixed request set taken from `bench/testcases/`, and compares capture off, capture on, and capture on with a saturated queue.
- **Reported figures.** The report gives:
  - p50, p95 and p99 latency;
  - allocations per request;
  - a significance test over repeated runs.
- **Thresholds.** The proposed acceptance threshold is no statistically significant change at p50 and under 5% change at p99. It will be written into the WP2 report before the measurement is run, and the measured numbers will be reported whether or not they meet it.

### WP3: Outcome labels

**Label entry.** An outcome label holds:

- the record ID;
- `source` and `trust_rank`. The sources, in order of trust, are:
  1. `user_confirmation`
  2. `user_annotation`
  3. `session_event`
- `observed_at`;
- `harm`: one of `harmful`, `benign` or `unknown`;
- `preferred_action`: optional;
- a short redacted note;
- for session events, the evidence (the event type and the time since the decision).

All labels are kept. A documented resolution rule picks the highest-trust label per record, and conflicts are reported, not hidden.

**Sources.**

- **User confirmation.** No plugin asks the user today, so this source produces no labels until a plugin does. See decision D1. The engine side will be built now:
  - a new endpoint, `POST /api/v1/decision-outcome`, taking `{session_id, event_id, outcome: approved|denied|timed_out}`;
  - a capture hook on the existing `/override` endpoint.

  Neither changes any verdict. Plugin changes wait for your decision.
- **User annotation.** This is a new CLI command, `agentshield decisions`, with these subcommands:
  - `list`
  - `show <id>`
  - `label <id> --harm harmful|benign [--preferred-action ...] [--note ...]`
  - `stats`
  - `purge` (deletes all captured data after confirmation)

  It reads and appends to the local store only.
- **Session events.** These are derived from captured lifecycle events. The proposed rules are:
  - an explicit session end with a reason, within a configurable window after a decision;
  - OpenClaw's `agent_end` with `success: false`.

  Claude Code's per-turn `session_end` is ignored unless it carries a reason. No rule reads prompt or output content. These labels are the weakest, and the WP3 report will measure how often each rule fires before any evaluation relies on them.

**Changed files.**

- `internal/decisionstore` gains a labels stream and reader.
- `internal/server/server.go`: the new endpoint and the capture hook on `/override`.
- `cmd/agentshield/main.go`: the `decisions` command.
- `docs/api.md`.
- `docs/decision-data/labels.md`.

### WP4: Evaluation harness

**New package: `internal/decisioneval`.** Given a set of records, the probabilities a decision maker assigned to each, and the outcome labels, it computes:

- **Precision and recall for "intervene".** At blockable points this means `require_approval` or `block`. At detection-only points it means raising an alert.
- **Calibration.** Expected calibration error (10 equal-width bins, with equal-mass bins as a secondary view), a reliability table (bin range, count, mean predicted probability, observed frequency) and the Brier score.
- **Split.** Every figure is split by `decision_point`.
- **What counts.** Records without an outcome label are excluded and counted. Reference labels are never used as targets.

**Replay changes.**

- `cmd/agentshield-replay` and `internal/replay` get a `--decision-records <dir>` source that reads the local store.
- Public benchmark traces are converted to records with `origin: replay`. The existing production-scoping caveat carries over. ATBench's trace-level labels come from the dataset authors, not from AgentShield's rules, so they count as outcome labels at trace level only.

**Protocol.** `docs/decision-data/EVAL_PROTOCOL.md` is written and committed before any comparison is run. It sets out:

- metric definitions;
- the positive class;
- the binning;
- the data splits: by session, in time order, with the calibration split disjoint from the test split, and a fixed seed;
- minimum sample sizes, below which a figure is reported as "insufficient data";
- pass thresholds.

Every report cites the protocol's commit hash. Any change to the protocol needs a new version and a full re-run. See decision D3.

### WP5: Decision interface and baselines

**New package: `internal/decisionmodel`.**

- **Interface.** `Model` has three methods: `Name()`, `Version()` and `Predict(ctx, *decisionrecord.Record) (Prediction, error)`. A `Prediction` is a probability for each action that is valid at the record's decision point:
  - all four actions at a blockable point;
  - alert or no alert at a detection-only point.

  It is read as the probability that each action is the correct one.
- **Baselines.** `RulesTriage` is the current system as a baseline, in two variants, both reported:
  1. One-hot on the recorded action. This reproduces deployed behaviour exactly, so its calibration error measures how overconfident a hard rule verdict is.
  2. A version that softens the probabilities using fast-triage confidence where triage ran.
- **Local open-weight adapter.** It calls a locally run model using the shared state-and-typed-questions request format.
  - The endpoint must resolve to a loopback address. This is checked when configuration is loaded, and any other address is refused.
  - No model is downloaded and no request is sent until decision D4 is settled.
- **Comparison.** Both are run through the WP4 harness. The report states the results and sample sizes without drawing conclusions the data does not support.

### WP6: From probabilities to actions

**New package: `internal/decisionpolicy`.**

- **Cost table.** It is configurable and indexed by (chosen action, correct action). For example, the cost of `allow` when `block` was correct, against the cost of `block` when `allow` was correct.
- **Selection rule.** Pick the action with the lowest expected cost: the sum, over each possible correct action b, of P(b) × C[a][b].
- **Calibration.** Temperature scaling fits a single temperature by minimising negative log-likelihood, on the calibration split only. A one-hot baseline needs a small documented smoothing before temperature scaling means anything, and the report says so.

**Shadow run.**

- **Offline first.** The policy runs over stored records with zero risk. The report compares what each system would have done, as an agreement matrix, the expected cost under the configured table, and the disagreements broken down by decision point.
- **Online shadow, if wanted.** It runs behind its own flag, separate from capture. It is asynchronous, after the response has been written, and records the shadow choice in a `shadow_decisions` field. A test asserts that responses do not change. See decision D5.

## 4. Decisions needed from you

- **D1: How to obtain the user's own confirmation decision.**
  - **The problem.** This is the highest-trust label source, but no plugin ever asks the user: `require_approval` is treated as a block everywhere. The only way to get these labels is for a plugin to hand `require_approval` to the host's own confirmation prompt and report the answer. For Claude Code, that means returning an "ask" permission decision from `PreToolUse`, and treating a `PostToolUse` carrying that `event_id` as approval. That changes what AgentShield blocks, from fail-closed to user-decides, so I will not do it without your approval.
  - **Options.**
    1. Leave the plugins unchanged and rely on annotations and session events for now.
    2. Approve the change for one plugin, behind an opt-in setting.
  - **Recommendation.** Option 1 for this piece of work, with the engine endpoint built so that option 2 can follow.
- **D2: Key management.**
  - **Proposal.** A locally generated key in a 0600 file next to the data, with `key_file` to move it elsewhere.
  - **Trade-off.** This protects against copied or backed-up data files when the key is kept apart from them. It does not protect against someone who can read the user's home directory. An OS keychain would be stronger, but it needs platform-specific code, and possibly CGO, which the project avoids.
  - **Question.** Is the key file acceptable for now?
- **D3: Pass thresholds for WP4.** I will propose concrete values in the protocol file, to be confirmed before any comparison runs. The values will cover:
  - minimum labelled records per decision point;
  - the maximum acceptable expected calibration error;
  - minimum recall at blockable points;
  - the margin a candidate must beat the baseline by.
- **D4: The local decision model.**
  - **What I need.** I have no specification for Clef-flash or for the shared state-and-typed-questions request format in this repository or environment. I need:
    - the format specification;
    - where the weights come from;
    - their licence;
    - which local runtime to target (for example llama.cpp or Ollama on localhost).
  - **Until then.** I will define the adapter against an interface and test it with a local stub. I will not download a model or adopt a dataset whose licence is not clearly permissive.
  - **Dataset licences.** For reference: ATBench is Apache-2.0 and the WildClaw traces are MIT. The nlile merged Claude Code traces declare no licence and merge ten sources, so I will not use them for evaluation without your confirmation.
- **D5: Online shadow scoring in WP6.** Offline shadow over stored records gives the comparison report with no runtime cost. Online shadow adds load from the local model, although off the response path. Do you want online shadow as well, or offline only for now?

## 5. Observations outside this scope

These were found during the survey. I have not changed them, because each one either affects detection or is unrelated to this work.

- **Unredacted arguments in SQLite.** The SQLite `alerts.args` column stores full argument maps unredacted and unencrypted. The `/evaluate` response also echoes the full field map in each alert's `matched_fields`.
- **Override rule cannot fire.** No plugin calls `/override`, so `session.override_count` is always zero and `ai_agent_override_escalation.yml` cannot fire.
- **Codex double evaluation.** Codex's `PermissionRequest` hook re-evaluates calls that `PreToolUse` already evaluated, under a new event ID. This probably defeats its own approval mode.
- **Claude Code session end.** Claude Code installs `Stop` rather than `SessionEnd`, so the engine receives a `session_end` after every turn.
- **OpenClaw session IDs.** OpenClaw uses `sessionKey` for tool events and `sessionId` for lifecycle events, so the two may not join.
- **Stale CLAUDE.md signature.** `CLAUDE.md` describes `NewServer` as taking five parameters. The current signature takes four, with no triager.
