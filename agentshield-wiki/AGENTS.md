# AGENTS.md

This file is the **contract** followed by LLM sessions that maintain `agentshield-wiki`. It
defines the page schema, attack taxonomy, and the three workflows (ingest, query, lint) that keep
the wiki useful over time.

If you are an LLM reading this: treat this file as authoritative. If something here contradicts
your instincts, follow this file and note the conflict in `log.md`.

---

## 1. Repository layout

```
agentshield-wiki/
├── AGENTS.md              ← this file
├── README.md              ← human-facing orientation
├── log.md                 ← append-only session journal
├── raw/                   ← immutable source captures
│   ├── papers/            ← academic research (arXiv, USENIX, IEEE S&P, …)
│   ├── advisories/        ← vendor advisories, CVEs, GHSAs
│   ├── incidents/         ← public post-mortems, breach reports
│   ├── blogs/             ← security blog posts & threat-intel write-ups
│   └── rules/             ← snapshots of external rule sets (Sigma, Semgrep, …)
└── wiki/
    ├── index.md           ← catalog of every page
    ├── overview.md        ← intro
    ├── coverage.md        ← TTP ↔ Sigma rule matrix
    ├── ttp/               ← one page per technique
    ├── rules/             ← one page per agentshield Sigma rule
    ├── vendors/           ← agent platforms, MCP servers, model providers
    └── synthesis/         ← cross-cutting analysis (trends, taxonomies, comparisons)
```

### `raw/` is immutable

Never modify a file under `raw/` after its initial commit. If a source is updated, add a new file
with a date suffix (e.g. `raw/advisories/acme-2026-04-05.md` → `…-2026-05-12.md`). Immutability is
the property that makes this wiki auditable.

---

## 2. Page types

Every page in `wiki/` is one of four types. Use the `type:` frontmatter field.

### 2.1 TTP page (`type: ttp`)

One technique per page, under `wiki/ttp/`. Describes **what the attacker does**, **what it looks
like on the wire**, and **what stops it**. Required sections:

- `## Summary` — 2–3 sentences.
- `## Mechanism` — how the attack works, step by step.
- `## Observables` — concrete signals: tool names, argument patterns, sequences, timing.
- `## Detection` — links to covering rules in `wiki/rules/`, or a `Coverage: none` note.
- `## Bypasses & variants` — what makes this hard to detect reliably.
- `## Sources` — links into `raw/` and external URLs.

### 2.2 Rule coverage page (`type: rule`)

One page per Sigma rule in `agentshield/rules/rules/ai_agent/`, under `wiki/rules/`. Required:

- `## What it matches` — plain-English description of the detection logic.
- `## TTPs covered` — links to `wiki/ttp/` pages.
- `## Known false positives` — benign patterns that trigger it.
- `## Known bypasses` — attacker techniques that evade it.
- `## Tuning notes` — thresholds, session-window params, triage hints.

### 2.3 Vendor / tool page (`type: vendor`)

One page per agent platform (Claude Code, Cursor, Aider, …), MCP server, model provider, or
integration. Under `wiki/vendors/`. Required:

- `## Surface area` — what tools/capabilities it exposes to the agent.
- `## Known attacks` — TTPs observed against this vendor.
- `## Hardening` — platform-specific defences and config.
- `## agentshield integration` — plugin status, canonical tool names, caveats.

### 2.4 Synthesis page (`type: synthesis`)

Cross-cutting analysis — trends, taxonomies, comparisons, lessons learned. Under
`wiki/synthesis/`. Freeform structure; must cite ≥ 3 TTP or rule pages.

---

## 3. Attack taxonomy

Every TTP page must carry a `category:` frontmatter field drawn from exactly these five values.
The product is always `ai_agent` (matching `logsource.product: ai_agent` in Sigma).

| category | Definition | Canonical examples |
|---|---|---|
| **execution** | Attacker-chosen instructions flow into the agent's decision loop and cause it to take actions it otherwise would not. | Direct / indirect prompt injection, tool-call hijacking, confused-deputy attacks, malicious system-prompt override. |
| **discovery** | Agent is steered to enumerate the host, its credentials, source code, or environment for attacker use. | `ls ~/.ssh`, env-var dumping, git-config inspection, cloud metadata queries, repo-wide secret grep. |
| **exfiltration** | Data leaves the trust boundary via the agent's own tools or outputs. | `curl` to attacker host, DNS OOB, markdown-image beacons, webhook POSTs, file-upload tools, agent response text. |
| **persistence** | Attacker plants instructions or configuration that will influence future agent sessions. | Poisoning `CLAUDE.md` / `AGENTS.md` / `.cursorrules`, installing a malicious MCP server, writing shell hooks, cron jobs, git hooks. |
| **impact** | Agent performs irreversible or destructive actions against the host, repo, or downstream systems. | `rm -rf`, `git push --force`, `DROP TABLE`, package unpublish, killing services, sending emails, posting on behalf of user. |

Notes:

- Many real attacks span multiple categories (e.g. recon-then-exfil). Pick the **primary**
  category for the page and cross-link via `chains:` frontmatter.
- `reconnaissance` is called `discovery` here to match MITRE ATT&CK conventions.
- `command-and-control` is **not** a separate category — C2 over the agent's tool channel is
  modelled as `execution` (inbound control) + `exfiltration` (outbound data).

---

## 4. Frontmatter schema

Every page starts with YAML frontmatter. Fields marked ★ are required.

```yaml
---
id: ttp.indirect-prompt-injection          # ★ stable slug, never change
type: ttp                                  # ★ ttp | rule | vendor | synthesis
title: Indirect Prompt Injection           # ★ human title
category: execution                        # ★ (ttp only) one of the 5 taxonomy values
chains: [discovery, exfiltration]          # (optional) secondary categories
status: draft                              # ★ draft | active | deprecated
severity: high                             # (ttp only) low | medium | high | critical
coverage: partial                          # (ttp only) none | partial | full
rules: [ai_agent_prompt_injection_url]     # (optional) rule IDs covering this TTP
ttps: [ttp.indirect-prompt-injection]      # (rule/vendor only) TTPs referenced
vendors: [claude-code, cursor]             # (optional) affected vendors
sources:                                   # (optional but encouraged) raw/ refs
  - raw/papers/greshake-2023-indirect-injection.md
  - https://arxiv.org/abs/2302.12173
created: 2026-04-05                        # ★ ISO date
updated: 2026-04-05                        # ★ ISO date, bump on every edit
---
```

---

## 5. Naming conventions

- **Slugs** (`id:` field and filename): lowercase, dot-separated namespace + kebab-case.
  - TTPs: `ttp.<kebab-name>` → `wiki/ttp/indirect-prompt-injection.md`
  - Rules: `rule.<sigma-rule-id>` → `wiki/rules/ai_agent_prompt_injection_url.md`
  - Vendors: `vendor.<name>` → `wiki/vendors/claude-code.md`
  - Synthesis: `synthesis.<kebab-topic>` → `wiki/synthesis/mcp-threat-model.md`
- **Rule IDs** mirror the Sigma rule filename (without `.yml`) exactly. Do not rename.
- **Raw captures**: `raw/<subdir>/<source>-YYYY-MM-DD[-slug].md`
- **File names are case-sensitive** and should match the `id:` field's final segment.

---

## 6. Workflows

### 6.1 Ingest workflow (new source)

Triggered when someone says "add this paper / advisory / blog / incident" or drops a URL.

1. **Capture to `raw/`.** Create `raw/<subdir>/<slug>-YYYY-MM-DD.md` with:
   - Frontmatter: `source_url`, `captured: YYYY-MM-DD`, `title`, `authors`, `venue`.
   - The full text (if fetched) or a structured summary with direct quotes for every claim you
     plan to use. Never paraphrase-only — future LLM sessions must be able to verify.
2. **Extract TTPs.** For each distinct technique in the source:
   - If a `wiki/ttp/<slug>.md` exists → append source ref to its `sources:` list, add any new
     observables/bypasses to the relevant sections, bump `updated:`.
   - Else → create a new TTP page (draft status is fine) with at minimum a Summary, Mechanism,
     and Sources section. Add to `wiki/index.md` and `wiki/coverage.md`.
3. **Cross-link vendors.** If the source names specific platforms/MCP servers, touch the relevant
   `wiki/vendors/` page (create if missing).
4. **Log it.** Append an entry to `log.md` (see §6.4).
5. **Commit.** One commit per source. Message: `ingest: <slug> (<type>)`.

### 6.2 Query workflow (answer a question)

Triggered when someone asks a substantive question about AI agent threats, coverage, or rules.

1. **Search before writing.** Grep `wiki/` for the topic. Read the matching pages in full.
2. **Answer from wiki content.** Cite page ids inline: `[ttp.indirect-prompt-injection]`.
3. **If the answer required inference beyond what's in the wiki**, and the inference is likely to
   be asked again, **file it back**:
   - Short fact → append to the most relevant existing page, bump `updated:`.
   - Cross-cutting insight → new `wiki/synthesis/` page.
   - Missing TTP → stub new `wiki/ttp/` page with `status: draft`, `coverage: none`, sources
     cited, and flag it in `coverage.md`.
4. **If the question revealed a coverage gap**, update `coverage.md` even if you don't stub the
   TTP page.
5. **Log it.** `log.md` entry summarising the question and what was filed back.

### 6.3 Lint workflow (periodic health check)

Run this workflow on demand ("lint the wiki") or weekly. Do not perform destructive fixes without
listing them first.

Checks:

1. **Frontmatter validity** — every page has required fields; `category:` is one of the 5
   canonical values; `updated:` ≥ `created:`; dates are valid ISO.
2. **ID / filename agreement** — `id:` final segment matches filename stem.
3. **Link integrity** — every internal link `[…](…)` resolves; every page referenced in
   frontmatter (`rules:`, `ttps:`, `vendors:`, `sources:`) exists.
4. **Index completeness** — every page in `wiki/` appears in `wiki/index.md` under the correct
   grouping.
5. **Coverage matrix freshness** — every `type: ttp` page appears in `coverage.md`; every rule
   listed there exists in the `agentshield` repo under `rules/rules/ai_agent/`.
6. **Orphans** — pages not linked from any other page or index.
7. **Raw backing** — every non-stub TTP page cites at least one `raw/` file or external URL.
8. **Staleness** — pages with `status: active` whose `updated:` is > 180 days old get flagged for
   review (not auto-deprecated).
9. **Taxonomy drift** — search for `category:` values outside the canonical 5.

Output: a report in `log.md` under a `## lint YYYY-MM-DD` heading. Propose fixes; apply only
non-destructive ones (adding missing index entries, fixing typos in frontmatter dates). Leave
content changes and deprecations for a human or a follow-up session.

### 6.4 `log.md` entry format

```markdown
## YYYY-MM-DD — <workflow>: <short title>

- **Session**: ingest | query | lint | freeform
- **Trigger**: (what the user asked, or "scheduled")
- **Touched**: list of page ids created/modified
- **Notes**: 1–3 bullets on anything non-obvious
```

Append only. Never rewrite prior entries.

---

## 7. Editing rules

- **One TTP per page, one rule per page.** If you feel the urge to split, do.
- **Prefer linking to duplicating.** Observables that apply to multiple TTPs live on whichever
  TTP page is most canonical; others link to it.
- **Mark uncertainty.** Use `_(unverified)_` inline for claims not yet backed by `raw/`.
- **Never invent rule IDs.** Only reference Sigma rules that actually exist in the `agentshield`
  repo. If proposing a new rule, use `proposed.<slug>` and list it under
  `coverage.md#proposed-rules`.
- **British English** for prose (matches parent repo convention). Code identifiers unchanged.
- **Do not delete pages.** Set `status: deprecated` and add a `deprecated_reason:` field.
