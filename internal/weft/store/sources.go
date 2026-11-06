package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
)

// Session sources: the producers that can record the same agent session,
// richest first, per harness. For Claude Code the weft-claude-code plugin
// reads the transcript (reasoning, tool results); Claude Code's own
// OpenTelemetry export sends traces (beta) and log events. For Codex the
// weft-codex plugin reads the rollout file; Codex's own export sends log
// events, and traces of its internals (dropped at ingest,
// otlp/codex_traces.go; ranked last should they ever be mapped onto a
// session). Gemini CLI has no Weft plugin: its log events
// rank above its traces (separate llm_call / tool_call spans). A user may
// turn on more than one, so
// each session is shown from one source only: the highest-ranked source of
// its harness that has sent spans for it. Spans of a lower
// source are stored hidden (soft-deleted) once a higher one is present, and
// are hidden when a higher one arrives (docs/weft/README.md, "Claude Code
// without a plugin"). Nothing is dropped, so a decoy cannot make telemetry
// disappear for good; every replacement is logged and reported
// (WithSourceReplacedHook: the server audits it).
//
// A span's source is its weft.source attribute (attributes.weft.source, the
// field the SQL below reads too); spans of any other source (or none) are
// never affected, nor are those of another harness.
//
// Who may claim a ranked source: a write under WithSourceClaims may only
// claim the ranked sources it lists (the server lists, per ingest path, the
// sources that path produces: the OTLP trace and log mappings set theirs,
// and a plugin's source needs the plugin's X-Weft-Source header). Any other
// ranked claim is kept as weft.claimed_source and does not rank. Writers
// without WithSourceClaims (internal ones) are trusted.
const (
	SourceClaudeCodePlugin = "weft-claude-code"
	SourceClaudeCodeTraces = "claude-code-otel-traces"
	SourceClaudeCodeLogs   = "claude-code-otel-logs"
	SourceCodexPlugin      = "weft-codex"
	SourceCodexTraces      = "codex-otel-traces"
	SourceCodexLogs        = "codex-otel-logs"
	SourceGeminiLogs       = "gemini-cli-otel-logs"
	SourceGeminiTraces     = "gemini-cli-otel-traces"
)

type rankedSource struct {
	harness string
	rank    int
}

var sessionSourceRank = map[string]rankedSource{
	SourceClaudeCodePlugin: {"claude-code", 3}, SourceClaudeCodeTraces: {"claude-code", 2}, SourceClaudeCodeLogs: {"claude-code", 1},
	SourceCodexPlugin: {"codex", 3}, SourceCodexLogs: {"codex", 2}, SourceCodexTraces: {"codex", 1},
	SourceGeminiLogs: {"gemini-cli", 2}, SourceGeminiTraces: {"gemini-cli", 1},
}

// SessionSourceRank is the precedence of a span source within its
// harness (0: not ranked).
func SessionSourceRank(source string) int { return sessionSourceRank[source].rank }

// sourcesRanked returns the sources of the same harness ranked above
// (higher) or below source.
func sourcesRanked(source string, higher bool) []string {
	me := sessionSourceRank[source]
	var out []string
	for s, r := range sessionSourceRank {
		if r.harness != me.harness {
			continue
		}
		if (higher && r.rank > me.rank) || (!higher && r.rank < me.rank) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

type sourceBatchKey struct{}

// sourceBatch remembers, within one write, the decision taken for each
// (project, session, source), so a batch checks a session once, the spans
// to store hidden, and the replacements made, logged once the write
// commits.
type sourceBatch struct {
	drop     map[[3]string]bool // (project, session, source) outranked: stored hidden
	retired  map[[3]string]bool // (project, session, source) that hid lower sources
	replaced []sourceReplacement
}

type sourceClaimsKey struct{}

// WithSourceClaims limits the ranked sources writes under ctx may claim
// (see the package comment above). No sources: no ranked claim stands.
func WithSourceClaims(ctx context.Context, allowed ...string) context.Context {
	m := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		m[a] = true
	}
	return context.WithValue(ctx, sourceClaimsKey{}, m)
}

// SourceReplaced describes a session whose lower-ranked spans were hidden.
type SourceReplaced struct {
	Project, Session, To string
	From                 map[string]int // hidden spans by source
	Relinked             int
}

type sourceHookKey struct{}

// WithSourceReplacedHook calls fn for each replacement once its write has
// committed (the server records it in the audit log).
func WithSourceReplacedHook(ctx context.Context, fn func(SourceReplaced)) context.Context {
	return context.WithValue(ctx, sourceHookKey{}, fn)
}

// RankedSources lists the ranked sources whose name ends in suffix
// ("-otel-traces", "-otel-logs"), for WithSourceClaims.
func RankedSources(suffix string) []string {
	var out []string
	for src := range sessionSourceRank {
		if strings.HasSuffix(src, suffix) {
			out = append(out, src)
		}
	}
	sort.Strings(out)
	return out
}

// IsPluginSource reports a source that only a Weft plugin sends (the top
// rank of its harness).
func IsPluginSource(src string) bool {
	r, ok := sessionSourceRank[src]
	return ok && strings.HasPrefix(src, "weft-") && r.rank == 3
}

// sourceReplacement is one session whose spans of lower-ranked sources were
// hidden because a higher-ranked source arrived.
type sourceReplacement struct {
	project, session, to string
	from                 map[string]int // hidden spans by source
	relinked             int            // detections linked again
}

// logSourceReplacements logs the replacements of a committed write (info:
// a session switches source once).
func logSourceReplacements(ctx context.Context) {
	b, _ := ctx.Value(sourceBatchKey{}).(*sourceBatch)
	if b == nil || len(b.replaced) == 0 {
		return
	}
	log := weftlog.From(ctx)
	for _, r := range b.replaced {
		froms := make([]string, 0, len(r.from))
		hidden := 0
		for f, n := range r.from {
			froms = append(froms, f)
			hidden += n
		}
		sort.Strings(froms)
		log.Info("session source replaced", "project", r.project, "session_id", r.session, "from", strings.Join(froms, ","), "to", r.to,
			"spans_hidden", hidden, "verdicts_relinked", r.relinked)
		if fn, ok := ctx.Value(sourceHookKey{}).(func(SourceReplaced)); ok {
			fn(SourceReplaced{Project: r.project, Session: r.session, To: r.to, From: r.from, Relinked: r.relinked})
		}
	}
	ingestCount(ctx, func(is *IngestStats) { is.SourcesReplaced += len(b.replaced) })
	b.replaced = nil
}

func withSourceBatch(ctx context.Context) context.Context {
	if _, ok := ctx.Value(sourceBatchKey{}).(*sourceBatch); ok {
		return ctx
	}
	return context.WithValue(ctx, sourceBatchKey{}, &sourceBatch{drop: map[[3]string]bool{}, retired: map[[3]string]bool{}})
}

// spanSource is a call's source: attributes.weft.source, the field
// sourceCondition reads in SQL (a flat "weft.source" key or an OTel
// attribute of that name is not a source). Gemini CLI's own trace spans
// (llm_call, tool_call, agent_call, ...; always exported alongside its log
// events) carry none: they are recognised by their OTel agent name and
// operation, as in sourceCondition, so the session built from the log
// events replaces them.
func spanSource(attributes map[string]any) string {
	if w, ok := attributes["weft"].(map[string]any); ok {
		if s, ok := w["source"].(string); ok && s != "" {
			return s
		}
		if _, ok := w["source"]; ok {
			return "" // a non-string source matches nothing in SQL either
		}
	}
	if o, ok := attributes["otel"].(map[string]any); ok {
		if a, ok := o["attributes"].(map[string]any); ok {
			if a["gen_ai.agent.name"] == geminiCLIAgent {
				if op, _ := a["gen_ai.operation.name"].(string); geminiTraceOps[op] {
					return SourceGeminiTraces
				}
			}
		}
	}
	return ""
}

// hiddenByKey marks, in attributes.weft, a span stored hidden because a
// higher-ranked source of its session was present (its value: that
// source's rank list). Set by the server only.
const hiddenByKey = "hidden_by_source"

// setWeftAttr returns st's attributes with attributes.weft[key] set (or
// removed for nil), copying the maps it changes.
func setWeftAttr(st *CallStart, key string, v any) {
	w, _ := st.Attributes["weft"].(map[string]any)
	w2 := make(map[string]any, len(w)+1)
	for k, x := range w {
		w2[k] = x
	}
	if v == nil {
		delete(w2, key)
	} else {
		w2[key] = v
	}
	attrs := make(map[string]any, len(st.Attributes)+1)
	for k, x := range st.Attributes {
		attrs[k] = x
	}
	attrs["weft"] = w2
	st.Attributes = attrs
}

// checkSourceClaim enforces WithSourceClaims on a call start: a ranked
// weft.source the write may not claim is moved to weft.claimed_source. A
// sender's own hidden_by_source is removed (the server sets it).
func checkSourceClaim(ctx context.Context, st *CallStart) {
	w, _ := st.Attributes["weft"].(map[string]any)
	if _, ok := w[hiddenByKey]; ok {
		setWeftAttr(st, hiddenByKey, nil)
	}
	allowed, ok := ctx.Value(sourceClaimsKey{}).(map[string]bool)
	if !ok {
		return
	}
	src, _ := w["source"].(string)
	if src == "" || sessionSourceRank[src].rank == 0 || allowed[src] {
		return
	}
	setWeftAttr(st, "source", nil)
	setWeftAttr(st, "claimed_source", src)
	weftlog.From(ctx).Debug("span source claim not accepted on this ingest path", "project", st.ProjectID, "span_id", st.ID, "claimed_source", src)
}

const geminiCLIAgent = "gemini-cli"

// geminiTraceOps are Gemini CLI's span operations (GeminiCliOperation).
var geminiTraceOps = map[string]bool{"llm_call": true, "tool_call": true, "schedule_tool_calls": true, "agent_call": true,
	"user_prompt": true, "system_prompt": true}

// sourceCondition is the SQL condition matching calls of the given sources
// (with their arguments).
func sourceCondition(sources []string) (string, []any) {
	ph, args := sourceList(sources)
	cond := `` + srcExpr + ` IN (` + ph + `)`
	for _, s := range sources {
		if s == SourceGeminiTraces {
			names := make([]string, 0, len(geminiTraceOps))
			for op := range geminiTraceOps {
				names = append(names, op)
			}
			sort.Strings(names)
			ops, opArgs := sourceList(names)
			cond = `(` + cond + ` OR (` + srcExpr + ` IS NULL
				AND ` + safeJSON(`'$.otel.attributes."gen_ai.agent.name"'`) + ` = ?
				AND ` + safeJSON(`'$.otel.attributes."gen_ai.operation.name"'`) + ` IN (` + ops + `)))`
			args = append(append(args, geminiCLIAgent), opArgs...)
		}
	}
	return cond, args
}

// srcExpr is a call's weft.source in SQL. json_extract fails on a row
// whose attributes are not valid JSON to SQLite (nested too deep): such a
// row has no source rather than failing every ingest of its session.
var srcExpr = safeJSON(`'$.weft.source'`)

func safeJSON(path string) string {
	return `(CASE WHEN json_valid(attributes) THEN json_extract(attributes, ` + path + `) END)`
}

// applySourcePrecedence decides how a call start of a ranked source is
// stored. It never drops it (drop is always false): it is stored hidden
// (attributes.weft.hidden_by_source, deleted_at set) when its session
// already has spans of a higher source; otherwise spans of lower sources
// there are hidden the same way (and detections linked to them are linked
// again). A ranked claim the write may not make is neutralised first
// (WithSourceClaims).
func (s *Store) applySourcePrecedence(ctx context.Context, tx *sql.Tx, st *CallStart) (drop bool, err error) {
	checkSourceClaim(ctx, st)
	if st.ThreadID == nil || *st.ThreadID == "" {
		return false, nil
	}
	src := spanSource(st.Attributes)
	if sessionSourceRank[src].rank == 0 {
		return false, nil
	}
	key := [3]string{st.ProjectID, *st.ThreadID, src}
	b, _ := ctx.Value(sourceBatchKey{}).(*sourceBatch)
	hide, cached := false, false
	if b != nil {
		hide, cached = b.drop[key]
	}
	if !cached {
		higher := sourcesRanked(src, true)
		hide, err = s.sessionHasSource(ctx, tx, st.ProjectID, *st.ThreadID, higher)
		if err != nil {
			return false, err
		}
		if hide {
			weftlog.From(ctx).Debug("spans hidden: the session has a higher-ranked source", "project", st.ProjectID, "session_id", *st.ThreadID,
				"source", src, "higher", strings.Join(higher, ","))
		}
		if b != nil {
			b.drop[key] = hide
		}
	}
	if hide {
		setWeftAttr(st, hiddenByKey, strings.Join(sourcesRanked(src, true), ","))
		return false, nil
	}
	if b != nil && b.retired[key] {
		return false, nil
	}
	rep, err := s.retireLowerSources(ctx, tx, st.ProjectID, *st.ThreadID, src, sourcesRanked(src, false))
	if err != nil {
		return false, err
	}
	if rep != nil && b != nil {
		rep.to = src
		b.replaced = append(b.replaced, *rep)
	}
	if b != nil {
		b.retired[key] = true
	}
	return false, nil
}

// hideIfOutranked hides a call start just written when applySourcePrecedence
// found its session outranked (calls.go runs it after the insert). It is
// counted in IngestStats.SpansDropped (hidden at ingest: stored, not shown).
func (s *Store) hideIfOutranked(ctx context.Context, tx *sql.Tx, st *CallStart) error {
	w, _ := st.Attributes["weft"].(map[string]any)
	if _, ok := w[hiddenByKey]; !ok {
		return nil
	}
	ingestCount(ctx, func(is *IngestStats) { is.SpansDropped++ })
	_, err := tx.ExecContext(ctx, `UPDATE calls SET deleted_at = COALESCE(deleted_at, ?) WHERE project_id = ? AND id = ?`, nowMicros(), st.ProjectID, st.ID)
	return err
}

// droppedEnd reports an end whose start was dropped (no stored call, and a
// drop marker): it is not stored either.
func (s *Store) droppedEnd(ctx context.Context, tx *sql.Tx, en *CallEnd) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM calls WHERE project_id = ? AND id = ?`, en.ProjectID, en.ID).Scan(&one)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM ingest_dropped WHERE project_id = ? AND id = ?`, en.ProjectID, en.ID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func sourceList(sources []string) (string, []any) {
	args := make([]any, len(sources))
	for i, s := range sources {
		args[i] = s
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(sources)), ","), args
}

func (s *Store) sessionHasSource(ctx context.Context, tx *sql.Tx, pid, session string, sources []string) (bool, error) {
	if len(sources) == 0 {
		return false, nil
	}
	cond, srcArgs := sourceCondition(sources)
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM calls WHERE project_id = ? AND thread_id = ? AND deleted_at IS NULL
		AND `+cond+` LIMIT 1`, append([]any{pid, session}, srcArgs...)...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// retireLowerSources soft-deletes a session's spans of the given sources and
// re-links the detections that pointed at them. It reports what it hid
// (nil when nothing).
func (s *Store) retireLowerSources(ctx context.Context, tx *sql.Tx, pid, session, to string, sources []string) (*sourceReplacement, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	cond, srcArgs := sourceCondition(sources)
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(`+srcExpr+`, '') FROM calls
		WHERE project_id = ? AND thread_id = ? AND deleted_at IS NULL AND `+cond, append([]any{pid, session}, srcArgs...)...)
	if err != nil {
		return nil, err
	}
	var ids []string
	from := map[string]int{}
	for rows.Next() {
		var id, src string
		if err := rows.Scan(&id, &src); err != nil {
			rows.Close()
			return nil, err
		}
		if src == "" {
			src = SourceGeminiTraces // recognised by its agent name (spanSource)
		}
		from[src]++
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return nil, err
	}
	now := nowMicros()
	var dets []string
	for start := 0; start < len(ids); start += 400 {
		chunk := ids[start:min(start+400, len(ids))]
		c, a := inList("id", chunk)
		if _, err := tx.ExecContext(ctx, `UPDATE calls SET deleted_at = ?,
				attributes = CASE WHEN json_valid(attributes) THEN json_set(attributes, '$.weft.`+hiddenByKey+`', ?) ELSE attributes END
			WHERE project_id = ? AND `+c, append([]any{now, to, pid}, a...)...); err != nil {
			return nil, err
		}
		c, a = inList("span_id", chunk)
		got, err := detQueryIDs(ctx, tx, `SELECT id FROM detections WHERE project_id = ? AND link <> 'replay' AND `+c, append([]any{pid}, a...)...)
		if err != nil {
			return nil, err
		}
		dets = append(dets, got...)
	}
	for _, d := range dets {
		if _, err := tx.ExecContext(ctx, `UPDATE detections SET span_id = NULL, link = '' WHERE project_id = ? AND id = ?`, pid, d); err != nil {
			return nil, err
		}
		if err := linkDetectionTx(ctx, tx, pid, d); err != nil {
			return nil, err
		}
	}
	return &sourceReplacement{project: pid, session: session, from: from, relinked: len(dets)}, nil
}
