# Labelled replay scoring

## Why this exists

The two datasets the replay tool originally supported, `nlile/misc-merged-claude-code-traces-v1`
and `sammshen/wildclaw-opus-traces`, are both benign. Nothing in them is supposed
to fire a rule, so a replay run measured false positives and nothing else. A rule
that detects nothing scored exactly like a correct rule.

`AI45Research/ATBench` (Apache-2.0) is a labelled corpus of 1,000 agent
trajectories, 497 labelled unsafe and 503 safe, which makes recall measurable.

## Running it

```bash
make replay-score
```

or directly:

```bash
./bin/agentshield-replay run \
  --dataset AI45Research/ATBench \
  --rules-dir rules/rules \
  --output atbench-scoring.json
```

Run the full split. ATBench rows are ordered by label: the first roughly 450 rows
are unsafe and the following roughly 450 are safe. Any `--max-traces` below that
returns a single-class prefix, which reports a precision of 1.0 that carries no
information. The report emits a `sample_warnings` entry when this happens, and
the run logs a warning, but the safest course is to score the whole split.

## What the metrics mean

### Everything headline is per trace

A malicious trace counts as detected when any event within it raises at least one
alert. A benign trace that alerts anywhere is one false positive. Precision,
recall, F1, accuracy and the false-positive rate are all computed over traces.

There is deliberately no per-event precision. The corpus labels are trace-level
and a malicious trace is mostly benign events. In trace id 1, for example, the
trajectory is labelled unsafe because the agent passed extracted web content into
`create_event` as authoritative, but the first call, `extract_news_article_text`,
is an ordinary fetch. Counting an alert on that fetch as a true positive would
inflate precision exactly when the rules are behaving worst, so it is not a second
view of the same truth but a metric that rewards the failure mode we are hunting.

What replaces it is `event_diagnostics`, a descriptive alert rate per 100 events
split by the label of the containing trace. It makes no ground-truth claim about
any individual event, and the field names say so.

`benign_alert_spread` buckets alerting benign traces by how many alerts each
raised. One noisy rule firing twenty times in a single session is a very different
problem from twenty sessions each tripping once, and a single false-positive count
hides the difference.

### Recall is reported twice

`metrics.recall` is what replay detects. `production_reproducible.metrics.recall`
is what the shipped system could detect on the same traces.

The gap exists because replay can see more than any producer sends. Two
production paths reach the rule engine:

- Pre-execution. A plugin builds an evaluation request
  (`plugins/openclaw/src/event-builder.ts`, `plugins/mcp-gateway/src/event-builder.ts`)
  carrying `event_type`, `tool_name`, `command` and `params`. The server enriches
  it (`internal/server/server.go`, `enrichEvent`) into `tool`, `event_type`,
  `context`, `source`, `command` and `file_path`, then copies every param into
  fields verbatim under its own key.
- Post-execution. A plugin posts a tool result to `/api/v1/audit`, and
  `internal/toolresult.DetectionFields` turns it into `event_type: tool_response`
  plus `response` and `content`, which the server runs a detection-only pass over.

Nothing in either path can populate a tool's declared `description`, a JSON blob
of a call's `arguments`, or `response_text`. Replay can populate all of them, and
does, because a rule should be given everything it could legitimately see. It then
re-evaluates each event a second time against a production-shaped request and
reports both numbers, so a detection that leans on a field no producer sends is
visible rather than silently inflating the headline figure.

`production_reproducible.field_provenance` lists which fields fall on each side.
`production_reproducible.detections_lost_to_replay_only_fields` counts the
malicious traces detected overall but not under production constraints.

This split is computed by re-running the rule engine, not by inspecting which
field a rule matched on. `engine.RuleResult.MatchedFields` is a copy of the whole
input map rather than the subset a rule keyed on, so it cannot answer the
question; re-evaluation can, and is exact.

The section is a pointer and is **omitted entirely** when the re-evaluation did
not run for every eligible event. This matters more than it sounds: a zeroed
production section reads as a full set of false negatives at recall 0.000, which
is byte-identical to the genuine finding that no rule can fire on production
fields. An absent section is honest, a zeroed one is not. Anything consuming the
report must handle its absence rather than assume a number.

### HTTP mode

`--http` evaluates each event twice against the running engine, once with full
fields and once production-shaped, so the production score is real rather than
assumed. That doubles the request count per event.

The engine rate-limits to roughly 1.7 requests per second per IP with a burst of
10, and that limit is not configurable, so `--http` sustains under one event per
second and drops the rest. Dropped events are counted in `scoring.dropped_events`
and raise a sample warning, because a malicious trace whose events were dropped
is scored as undetected, which makes recall a lower bound rather than a
measurement. For bulk scoring use library mode, which is what `make replay-score`
and the workflow do. Reserve `--http` for asking a specific deployed engine about
a small sample.

### The safe label does not mean no attack

Half the safe-labelled traces carry an attack. Only 250 of the 503 have
`risk_source: benign`; the rest are attacked-but-resisted trajectories, including
50 whose tool descriptions are genuinely poisoned. ATBench's label records whether
the agent came to harm, not whether an attack was present.

A monitoring tool arguably should alert on a poisoned tool description even when
the agent resists it, so an alert on one of those traces is not obviously an
error. `benign_by_risk_source` therefore splits the false positives by
`risk_source`, so alerts on genuinely clean traces can be separated from alerts on
attacked-but-resisted ones before anyone decides which are bugs.

## Interpreting a low recall

Near-zero recall has two very different causes that look identical in the summary:
a real blind spot in the rule corpus, or a field-mapping artefact where the rules
never received what they needed. Telling them apart is the point of the exercise.

Use `--dump-fields` to settle it:

```bash
./bin/agentshield-replay run --dataset AI45Research/ATBench \
  --rules-dir rules/rules --output /dev/null \
  --dump-fields fields.jsonl --dump-fields-limit 6000
```

Each line carries the exact field map handed to the engine, the
production-shaped subset, and the rules that matched each. Read those against the
`detection:` blocks of the rules that ought to have matched. If the literal a rule
looks for is absent from the field it reads, the corpus does not contain that
attack pattern and the miss is real. If the literal is present but in a different
field, the mapping is at fault.

## Tool surface

ATBench's tools are SaaS-shaped: event logging, calendars, web extraction, SMS,
QR codes. There are 1,953 distinct tool names across 2,972 calls, and not one of
them matches a case in `mapToolCall`, so every call takes the default branch and
`command` is the bare tool name.

That is faithful rather than lossy. `normaliseToolCall` in
`plugins/openclaw/src/normalise.ts` also falls through to the bare tool name for
any tool it does not recognise, so production sees exactly the same string.
Synthesising a richer command in replay would make replay detect things the
shipped system cannot, which is the failure the provenance split exists to
prevent.

The consequence is that the 38 rules keyed on `command|contains` with shell
literals cannot match this corpus, because these are API calls rather than shell
commands. That is a scope observation about the rule corpus, not a defect in the
adapter.

## Dataset shape

Verified across all 1,000 rows of config `ATBench`, split `test`:

| Property | Value |
| --- | --- |
| Rows | 1,000, ids 1 to 1,000, all unique |
| Labels | 497 unsafe, 503 safe, ordered by label |
| Conversations per row | always exactly 1 |
| Turn shapes | 3 only: `{role: user, content}`, `{role: environment, content}`, `{role: agent, thought, action}` |
| Agent actions | 2,972 tool calls, 1,520 `Complete{...}` sentinels |
| Sentinel prefixes | `Complete` only |
| Complete position | 546 of 1,520 are not the final agent turn |
| `arguments` encoding | 2,966 objects, 6 JSON-encoded strings |
| Environment turns | 2,971, every one immediately following a tool call |
| Calls without a response | 1, the final turn of one trace |
| Poisoned descriptions | 113 rows, 63 unsafe and 50 safe |

Two of these matter for parsing. `Complete` terminates an episode rather than the
conversation, so an adapter that stops at the first one silently truncates the
trace. And `arguments` is occasionally a JSON string that needs decoding twice.

Where a description has been poisoned, `description` holds the attacker's text and
`_original_description` preserves the clean version. The adapter uses
`description`, because that is what the agent saw. The difference between the two
identifies exactly which tool was tampered with.
