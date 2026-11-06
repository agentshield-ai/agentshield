package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// CallStart is the start half of a call.
type CallStart struct {
	ProjectID   string         `json:"project_id"`
	ID          string         `json:"id"`
	OpName      string         `json:"op_name"`
	DisplayName *string        `json:"display_name"`
	TraceID     string         `json:"trace_id"`
	ParentID    *string        `json:"parent_id"`
	ThreadID    *string        `json:"thread_id"`
	TurnID      *string        `json:"turn_id"`
	StartedAt   string         `json:"started_at"`
	Attributes  map[string]any `json:"attributes"`
	Inputs      map[string]any `json:"inputs"`
	UserID      *string        `json:"user_id"`
	RunID       *string        `json:"run_id"`
	RunStep     *int64         `json:"run_step"`
	// ReplaceDisplayName makes a re-sent start replace a stored display
	// name (spans rebuilt from an event log, eventlog.go); otherwise the
	// first one, or a rename, is kept.
	ReplaceDisplayName bool `json:"-"`
}

// CallEnd is the end half of a call.
type CallEnd struct {
	ProjectID string         `json:"project_id"`
	ID        string         `json:"id"`
	EndedAt   string         `json:"ended_at"`
	Exception *string        `json:"exception"`
	Output    any            `json:"output"`
	Summary   map[string]any `json:"summary"`
	// RunStepEnd is the linked run's step when the call ended.
	RunStepEnd *int64 `json:"run_step_end"`
	// Cumulative marks a summary that already includes descendants' usage.
	Cumulative bool `json:"-"`
}

// CompleteCall is a call delivered in one piece (OTLP spans, server-side calls).
type CompleteCall struct {
	Start CallStart
	// End may be nil for a call that is still running.
	End *CallEnd
}

// BatchItem is one entry of /call/upsert_batch.
type BatchItem struct {
	Mode  string
	Start *CallStart
	End   *CallEnd
}

// EndedCall identifies a call that just finished; used to trigger monitors.
type EndedCall struct {
	ProjectID string
	ID        string
}

const maxIDLen = 256

func validID(s string) bool {
	if s == "" || len(s) > maxIDLen {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

// OpBaseName returns the name part of an op ref, or the op name itself.
func OpBaseName(opName string) string {
	if refs.IsRef(opName) {
		if r, err := refs.Parse(opName); err == nil {
			return r.Name
		}
	}
	return opName
}

func (st *CallStart) normalize() error {
	p, err := refs.NormalizeProject(st.ProjectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	st.ProjectID = p
	if !validID(st.ID) {
		return Invalidf("invalid span id %q", st.ID)
	}
	if st.TraceID == "" {
		st.TraceID = st.ID
	}
	if !validID(st.TraceID) {
		return Invalidf("invalid trace id %q", st.TraceID)
	}
	if st.ParentID != nil && *st.ParentID == "" {
		st.ParentID = nil
	}
	if st.ParentID != nil && (!validID(*st.ParentID) || *st.ParentID == st.ID) {
		return Invalidf("invalid parent id")
	}
	st.OpName = refs.Normalize(strings.TrimSpace(st.OpName))
	if st.OpName == "" {
		return Invalidf("op_name is required")
	}
	if len(st.OpName) > 2048 {
		return Invalidf("op_name too long")
	}
	for _, p := range []**string{&st.ThreadID, &st.TurnID, &st.DisplayName, &st.UserID, &st.RunID} {
		if *p != nil && **p == "" {
			*p = nil
		}
	}
	if st.Inputs == nil {
		st.Inputs = map[string]any{}
	}
	if st.Attributes == nil {
		st.Attributes = map[string]any{}
	}
	// An explicit session_id wins; else the weft.session_id attribute (or
	// its deprecated name), nested or flat, as OTLP ingest reads it.
	if st.ThreadID == nil {
		if v := newSpanAttrs(st.Attributes).str(AttrKeys(AttrSessionID)...); v != "" {
			st.ThreadID = &v
		}
	}
	return nil
}

func (en *CallEnd) normalize() error {
	p, err := refs.NormalizeProject(en.ProjectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	en.ProjectID = p
	if !validID(en.ID) {
		return Invalidf("invalid span id %q", en.ID)
	}
	if en.Summary == nil {
		en.Summary = map[string]any{}
	}
	if en.Exception != nil && *en.Exception == "" {
		s := "error"
		en.Exception = &s
	}
	return nil
}

// StartCall records the start of a call.
func (s *Store) StartCall(ctx context.Context, st CallStart) error {
	if err := st.normalize(); err != nil {
		return err
	}
	s.hookStart(&st)
	s.nerRedact(ctx, []*CallStart{&st}, nil)
	ctx = withSourceBatch(ctx) // sources.go: logs a replacement once it commits
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, st.ProjectID); err != nil {
			return err
		}
		return s.applyStart(ctx, tx, &st)
	})
	if err == nil {
		logSourceReplacements(ctx) // sources.go
	}
	return err
}

// EndCall records the end of a call.
func (s *Store) EndCall(ctx context.Context, en CallEnd) error {
	if err := en.normalize(); err != nil {
		return err
	}
	s.nerRedact(ctx, nil, []*CallEnd{&en})
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, en.ProjectID); err != nil {
			return err
		}
		return s.applyEnd(ctx, tx, &en)
	})
}

// UpsertBatch applies a batch of starts and ends atomically and returns the
// calls that ended in this batch.
func (s *Store) UpsertBatch(ctx context.Context, items []BatchItem) ([]EndedCall, error) {
	for i := range items {
		switch items[i].Mode {
		case "start":
			if items[i].Start == nil {
				return nil, Invalidf("batch item %d: missing start", i)
			}
			if err := items[i].Start.normalize(); err != nil {
				return nil, err
			}
			s.hookStart(items[i].Start)
		case "end":
			if items[i].End == nil {
				return nil, Invalidf("batch item %d: missing end", i)
			}
			if err := items[i].End.normalize(); err != nil {
				return nil, err
			}
		default:
			return nil, Invalidf("batch item %d: mode must be start or end", i)
		}
	}
	if s.pii != nil {
		var sts []*CallStart
		var ens []*CallEnd
		for _, it := range items {
			if it.Start != nil {
				sts = append(sts, it.Start)
			}
			if it.End != nil {
				ens = append(ens, it.End)
			}
		}
		s.nerRedact(ctx, sts, ens)
	}
	var ended []EndedCall
	ctx = withTraceBatch(ctx)  // agents_handoffs.go: derive each touched trace once
	ctx = withSourceBatch(ctx) // sources.go: check each session's source once
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		touched := map[string]bool{}
		for _, it := range items {
			var pid string
			if it.Start != nil {
				pid = it.Start.ProjectID
			} else {
				pid = it.End.ProjectID
			}
			if !touched[pid] {
				touched[pid] = true
				if err := s.touchProject(ctx, tx, pid); err != nil {
					return err
				}
			}
			if it.Mode == "start" {
				if err := s.applyStart(ctx, tx, it.Start); err != nil {
					return err
				}
			} else {
				if err := s.applyEnd(ctx, tx, it.End); err != nil {
					return err
				}
				ended = append(ended, EndedCall{ProjectID: it.End.ProjectID, ID: it.End.ID})
			}
		}
		return flushTraceBatch(ctx, tx)
	})
	if err == nil {
		logSourceReplacements(ctx) // sources.go
	}
	return ended, err
}

// UpsertComplete writes calls that arrive whole (start and end together).
func (s *Store) UpsertComplete(ctx context.Context, calls []CompleteCall) ([]EndedCall, error) {
	for i := range calls {
		if err := calls[i].Start.normalize(); err != nil {
			return nil, err
		}
		s.hookStart(&calls[i].Start)
		if calls[i].End != nil {
			calls[i].End.ProjectID = calls[i].Start.ProjectID
			calls[i].End.ID = calls[i].Start.ID
			if err := calls[i].End.normalize(); err != nil {
				return nil, err
			}
		}
	}
	if s.pii != nil {
		var sts []*CallStart
		var ens []*CallEnd
		for i := range calls {
			sts = append(sts, &calls[i].Start)
			if calls[i].End != nil {
				ens = append(ens, calls[i].End)
			}
		}
		s.nerRedact(ctx, sts, ens)
	}
	var ended []EndedCall
	ctx = withTraceBatch(ctx)  // agents_handoffs.go: derive each touched trace once
	ctx = withSourceBatch(ctx) // sources.go: check each session's source once
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		touched := map[string]bool{}
		for i := range calls {
			c := &calls[i]
			if !touched[c.Start.ProjectID] {
				touched[c.Start.ProjectID] = true
				if err := s.touchProject(ctx, tx, c.Start.ProjectID); err != nil {
					return err
				}
			}
			if err := s.applyStart(ctx, tx, &c.Start); err != nil {
				return err
			}
			if c.End != nil {
				if err := s.applyEnd(ctx, tx, c.End); err != nil {
					return err
				}
				ended = append(ended, EndedCall{ProjectID: c.Start.ProjectID, ID: c.Start.ID})
			}
		}
		return flushTraceBatch(ctx, tx)
	})
	if err == nil {
		logSourceReplacements(ctx) // sources.go
	}
	return ended, err
}

func (s *Store) applyStart(ctx context.Context, tx *sql.Tx, st *CallStart) error {
	if drop, err := s.applySourcePrecedence(ctx, tx, st); drop || err != nil { // sources.go
		if drop && err == nil {
			ingestCount(ctx, func(is *IngestStats) { is.SpansDropped++ })
		}
		return err
	}
	if drop, err := ingestHook(ctx, s, tx, st, nil); drop || err != nil {
		if drop && err == nil {
			ingestCount(ctx, func(is *IngestStats) { is.SpansSampled++ })
		}
		return err
	}
	ingestCount(ctx, func(is *IngestStats) {
		is.SpansStored++
		if st.ThreadID != nil {
			is.addSession(*st.ThreadID)
		}
	})
	started, err := ParseTime(st.StartedAt)
	if err != nil {
		return err
	}
	if err := s.detachFromOldParent(ctx, tx, st); err != nil {
		return err
	}
	inputs := mustJSON(st.Inputs)
	attrs := mustJSON(st.Attributes)
	inRefs := mustJSON(nonNil(normRefs(refs.CollectRefs(st.Inputs))))
	_, err = tx.ExecContext(ctx, `INSERT INTO calls(project_id, id, op_name, op_base_name, display_name, trace_id, parent_id,
			thread_id, turn_id, started_at, attributes, inputs, input_refs, user_id, run_id, has_start, storage_size)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?)
		ON CONFLICT(project_id, id) DO UPDATE SET
			op_name = excluded.op_name, op_base_name = excluded.op_base_name,
			display_name = CASE WHEN ? THEN excluded.display_name ELSE COALESCE(calls.display_name, excluded.display_name) END,
			trace_id = excluded.trace_id, parent_id = excluded.parent_id,
			thread_id = excluded.thread_id, turn_id = excluded.turn_id, started_at = excluded.started_at,
			attributes = excluded.attributes, inputs = excluded.inputs, input_refs = excluded.input_refs,
			user_id = excluded.user_id, run_id = excluded.run_id, has_start = 1,
			storage_size = calls.storage_size - (CASE WHEN calls.has_start = 1 THEN length(calls.inputs) + length(calls.attributes) ELSE 0 END) + excluded.storage_size`,
		st.ProjectID, st.ID, st.OpName, OpBaseName(st.OpName), nullStr(st.DisplayName), st.TraceID, nullStr(st.ParentID),
		nullStr(st.ThreadID), nullStr(st.TurnID), started, attrs, inputs, inRefs, nullStr(st.UserID), nullStr(st.RunID),
		len(inputs)+len(attrs), st.ReplaceDisplayName)
	if err != nil {
		return err
	}
	if err := setRunStep(ctx, tx, st.ProjectID, st.ID, "run_step", st.RunStep); err != nil { // callfields.go
		return err
	}
	if err := s.absorbChildren(ctx, tx, st.ProjectID, st.ID); err != nil {
		return err
	}
	if err := s.propagate(ctx, tx, st.ProjectID, st.ID); err != nil {
		return err
	}
	return runCallHooks(ctx, tx, st.ProjectID, st.ID)
}

func (s *Store) applyEnd(ctx context.Context, tx *sql.Tx, en *CallEnd) error {
	if drop, err := s.droppedEnd(ctx, tx, en); drop || err != nil { // sources.go
		return err
	}
	if drop, err := ingestHook(ctx, s, tx, nil, en); drop || err != nil {
		return err
	}
	ended, err := ParseTime(en.EndedAt)
	if err != nil {
		return err
	}
	var output any
	outStr := ""
	if en.Output != nil {
		outStr = mustJSON(en.Output)
		output = outStr
	}
	summary := mustJSON(en.Summary)
	outRefs := mustJSON(nonNil(normRefs(refs.CollectRefs(en.Output))))
	_, err = tx.ExecContext(ctx, `INSERT INTO calls(project_id, id, trace_id, ended_at, exception, output, summary, output_refs, storage_size, cumulative)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, id) DO UPDATE SET
			ended_at = excluded.ended_at, exception = excluded.exception, output = excluded.output,
			summary = excluded.summary, output_refs = excluded.output_refs, cumulative = excluded.cumulative,
			storage_size = calls.storage_size - length(COALESCE(calls.output, '')) - length(calls.summary) + excluded.storage_size`,
		en.ProjectID, en.ID, en.ID, ended, nullStr(en.Exception), output, summary, outRefs, len(outStr)+len(summary), en.Cumulative)
	if err != nil {
		return err
	}
	if err := setRunStep(ctx, tx, en.ProjectID, en.ID, "run_step_end", en.RunStepEnd); err != nil { // callfields.go
		return err
	}
	if err := s.refreshTotalTokens(ctx, tx, en.ProjectID, en.ID); err != nil {
		return err
	}
	if err := s.propagate(ctx, tx, en.ProjectID, en.ID); err != nil {
		return err
	}
	return runCallHooks(ctx, tx, en.ProjectID, en.ID)
}

// callHooks run inside the write transaction after a call's start or end is
// stored (feature modules register them from init with registerCallHook).
var callHooks []func(ctx context.Context, tx *sql.Tx, projectID, id string) error

func registerCallHook(fn func(ctx context.Context, tx *sql.Tx, projectID, id string) error) {
	callHooks = append(callHooks, fn)
}

func runCallHooks(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	for _, h := range callHooks {
		if err := h(ctx, tx, projectID, id); err != nil {
			return err
		}
	}
	return nil
}

// ingestHook runs before a call's start or end is stored (exactly one of st,
// en is set). It may rewrite the call (redaction) or drop it (sampling).
// projectadmin.go installs the real hook.
var ingestHook = func(context.Context, *Store, *sql.Tx, *CallStart, *CallEnd) (drop bool, err error) { return false, nil }

// normRefs writes refs with the current kind names (refs.Normalize).
func normRefs(xs []string) []string {
	for i, x := range xs {
		xs[i] = refs.Normalize(x)
	}
	return xs
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// ---------------------------------------------------------------- roll-up
//
// Each call stores:
//   own:        derived from its summary.usage and its status once ended
//   rollup:     the sum of total(child) for children already linked
//   propagated: the part of total(self) already added to the parent's rollup
// where total = own + rollup. The invariant for a call whose parent exists is
// propagated == total, so every ancestor sees the sum of its descendants
// regardless of the order in which calls arrive.

type rollState struct {
	parentID   sql.NullString
	summary    string
	endedAt    sql.NullInt64
	exception  sql.NullString
	cumulative bool
	rollup     Contribution
	propagated Contribution
}

func (s *Store) loadRoll(ctx context.Context, tx *sql.Tx, projectID, id string) (*rollState, error) {
	var st rollState
	var rollup, prop string
	err := tx.QueryRowContext(ctx, `SELECT parent_id, summary, ended_at, exception, cumulative, rollup, propagated FROM calls WHERE project_id = ? AND id = ?`,
		projectID, id).Scan(&st.parentID, &st.summary, &st.endedAt, &st.exception, &st.cumulative, &rollup, &prop)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.rollup = ParseContribution(rollup)
	st.propagated = ParseContribution(prop)
	return &st, nil
}

func (st *rollState) total() Contribution {
	return TotalContribution(decodeMap(st.summary), st.endedAt.Valid, st.exception.Valid, st.cumulative, st.rollup)
}

func (s *Store) saveRollup(ctx context.Context, tx *sql.Tx, projectID, id string, st *rollState) error {
	total := st.total()
	_, err := tx.ExecContext(ctx, `UPDATE calls SET rollup = ?, total_tokens = ? WHERE project_id = ? AND id = ?`,
		st.rollup.JSON(), int64(total.Tokens()), projectID, id)
	return err
}

func (s *Store) refreshTotalTokens(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	st, err := s.loadRoll(ctx, tx, projectID, id)
	if err != nil || st == nil {
		return err
	}
	return s.saveRollup(ctx, tx, projectID, id, st)
}

// absorbChildren links children that arrived before this call.
func (s *Store) absorbChildren(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, summary, ended_at, exception, cumulative, rollup, propagated FROM calls WHERE project_id = ? AND parent_id = ?`,
		projectID, id)
	if err != nil {
		return err
	}
	type upd struct {
		id   string
		prop Contribution
	}
	var delta Contribution
	var updates []upd
	for rows.Next() {
		var cid, summary, rollup, prop string
		var ended sql.NullInt64
		var exc sql.NullString
		var cum bool
		if err := rows.Scan(&cid, &summary, &ended, &exc, &cum, &rollup, &prop); err != nil {
			rows.Close()
			return err
		}
		total := TotalContribution(decodeMap(summary), ended.Valid, exc.Valid, cum, ParseContribution(rollup))
		d := total.Clone()
		d.Sub(ParseContribution(prop))
		if !d.IsZero() {
			delta.Add(d)
			updates = append(updates, upd{cid, total})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return s.refreshTotalTokens(ctx, tx, projectID, id)
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE calls SET propagated = ? WHERE project_id = ? AND id = ?`, u.prop.JSON(), projectID, u.id); err != nil {
			return err
		}
	}
	st, err := s.loadRoll(ctx, tx, projectID, id)
	if err != nil || st == nil {
		return err
	}
	st.rollup.Add(delta)
	return s.saveRollup(ctx, tx, projectID, id, st)
}

// detachFromOldParent handles a call sent again with a different parent
// (a producer that learns the parent later, such as Claude Code's log
// events): what the call had added to its old parent's roll-up is taken out
// of the old ancestor chain, so that propagate adds it under the new parent
// and nothing is counted twice.
func (s *Store) detachFromOldParent(ctx context.Context, tx *sql.Tx, st *CallStart) error {
	old, err := s.loadRoll(ctx, tx, st.ProjectID, st.ID)
	if err != nil || old == nil || !old.parentID.Valid || old.parentID.String == "" {
		return err
	}
	if st.ParentID != nil && *st.ParentID == old.parentID.String {
		return nil
	}
	if old.propagated.IsZero() {
		return nil
	}
	parent, err := s.loadRoll(ctx, tx, st.ProjectID, old.parentID.String)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE calls SET propagated = '{}' WHERE project_id = ? AND id = ?`, st.ProjectID, st.ID); err != nil {
		return err
	}
	if parent == nil {
		return nil
	}
	parent.rollup.Sub(old.propagated)
	if err := s.saveRollup(ctx, tx, st.ProjectID, old.parentID.String, parent); err != nil {
		return err
	}
	return s.propagate(ctx, tx, st.ProjectID, old.parentID.String)
}

// propagate pushes any unpropagated change in total(id) up the ancestor chain.
func (s *Store) propagate(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	seen := map[string]bool{}
	for depth := 0; depth < 10000; depth++ {
		if seen[id] {
			return nil // cycle guard
		}
		seen[id] = true
		st, err := s.loadRoll(ctx, tx, projectID, id)
		if err != nil || st == nil {
			return err
		}
		if !st.parentID.Valid || st.parentID.String == "" {
			return nil
		}
		total := st.total()
		d := total.Clone()
		d.Sub(st.propagated)
		if d.IsZero() {
			return nil
		}
		parent, err := s.loadRoll(ctx, tx, projectID, st.parentID.String)
		if err != nil {
			return err
		}
		if parent == nil {
			return nil // parent will absorb us when it arrives
		}
		if _, err := tx.ExecContext(ctx, `UPDATE calls SET propagated = ? WHERE project_id = ? AND id = ?`, total.JSON(), projectID, id); err != nil {
			return err
		}
		parent.rollup.Add(d)
		if err := s.saveRollup(ctx, tx, projectID, st.parentID.String, parent); err != nil {
			return err
		}
		id = st.parentID.String
	}
	return nil
}

// ---------------------------------------------------------------- updates

// DeleteCalls soft-deletes calls and all their descendants.
func (s *Store) DeleteCalls(ctx context.Context, projectID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > 1000 {
		return Invalidf("too many span ids (max 1000)")
	}
	now := nowMicros()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := []any{projectID}
		for _, id := range ids {
			args = append(args, id)
		}
		args = append(args, projectID, now, projectID)
		_, err := tx.ExecContext(ctx, `WITH RECURSIVE sub(id) AS (
				SELECT id FROM calls WHERE project_id = ? AND id IN (`+ph+`)
				UNION SELECT c.id FROM calls c JOIN sub ON c.parent_id = sub.id WHERE c.project_id = ?
			)
			UPDATE calls SET deleted_at = ? WHERE project_id = ? AND deleted_at IS NULL AND id IN (SELECT id FROM sub)`, args...)
		return err
	})
}

// UpdateCallDisplayName renames a call.
func (s *Store) UpdateCallDisplayName(ctx context.Context, projectID, id, name string) error {
	if len(name) > 1024 {
		return Invalidf("display_name too long")
	}
	var v any = name
	if name == "" {
		v = nil
	}
	res, err := s.w.ExecContext(ctx, `UPDATE calls SET display_name = ? WHERE project_id = ? AND id = ?`, v, projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
