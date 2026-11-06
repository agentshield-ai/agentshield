package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
)

// Undoing source hiding (sources.go). A session's spans of a lower-ranked
// source are hidden, not dropped, when a higher-ranked source sends spans
// for it, and a Weft plugin's source is claimed by a header any project
// writer can send. Two things bring hidden spans back:
//
//   - Deleting the spans that outrank them: when a session's spans of a
//     ranked source are deleted (DeleteCalls), the session is shown again
//     from the highest-ranked source it still has, hidden spans included.
//     Hidden spans a deletion names itself stay deleted.
//   - A project admin's restore (RestoreSessionSources): the session stops
//     honouring plugin claims (X-Weft-Source) for good; its plugin spans
//     keep their content but lose their rank (weft.claimed_source), and
//     its hidden spans are shown again.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS session_source_settings (
	project_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	ignore_plugin_claims INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL,
	updated_by TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, session_id)
);
`)
}

// SessionSourceCount is one source of a session.
type SessionSourceCount struct {
	Source string `json:"source"`
	Rank   int    `json:"rank"`
	// Spans counts its shown spans, Hidden those hidden because a
	// higher-ranked source was present.
	Spans  int `json:"spans"`
	Hidden int `json:"hidden"`
}

// SessionSources describes the ranked sources of one session.
type SessionSources struct {
	SessionID string               `json:"session_id"`
	Sources   []SessionSourceCount `json:"sources"`
	// IgnorePluginClaims: a project admin turned off plugin claims
	// (X-Weft-Source) for this session.
	IgnorePluginClaims bool `json:"ignore_plugin_claims"`
	// Restored counts spans shown again by the request (restore only).
	Restored int `json:"restored,omitempty"`
}

var hiddenExpr = safeJSON(`'$.weft.` + hiddenByKey + `'`)

func allRankedSources() []string {
	out := make([]string, 0, len(sessionSourceRank))
	for s := range sessionSourceRank {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// sessionSourceCounts counts a session's spans by ranked source.
func sessionSourceCounts(ctx context.Context, tx detDB, pid, session string) (map[string]*SessionSourceCount, error) {
	cond, args := sourceCondition(allRankedSources())
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(`+srcExpr+`, ''), deleted_at IS NULL, `+hiddenExpr+` IS NOT NULL, COUNT(*) FROM calls
		WHERE project_id = ? AND thread_id = ? AND `+cond+` GROUP BY 1, 2, 3`, append([]any{pid, session}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*SessionSourceCount{}
	for rows.Next() {
		var src string
		var live, hidden bool
		var n int
		if err := rows.Scan(&src, &live, &hidden, &n); err != nil {
			return nil, err
		}
		if src == "" {
			src = SourceGeminiTraces // recognised by its agent name (spanSource)
		}
		c := out[src]
		if c == nil {
			c = &SessionSourceCount{Source: src, Rank: SessionSourceRank(src)}
			out[src] = c
		}
		switch {
		case live:
			c.Spans += n
		case hidden:
			c.Hidden += n
		}
	}
	return out, rows.Err()
}

// restoreHiddenSources shows a session again from the highest-ranked
// source it has, per harness, when that source's spans are hidden (the
// source that outranked them is gone or no longer ranks). It returns the
// spans restored.
func (s *Store) restoreHiddenSources(ctx context.Context, tx *sql.Tx, pid, session string) (int, error) {
	counts, err := sessionSourceCounts(ctx, tx, pid, session)
	if err != nil {
		return 0, err
	}
	type best struct {
		live, hidden string
	}
	byHarness := map[string]*best{}
	for src, c := range counts {
		h := sessionSourceRank[src].harness
		b := byHarness[h]
		if b == nil {
			b = &best{}
			byHarness[h] = b
		}
		if c.Spans > 0 && SessionSourceRank(src) > SessionSourceRank(b.live) {
			b.live = src
		}
		if c.Hidden > 0 && SessionSourceRank(src) > SessionSourceRank(b.hidden) {
			b.hidden = src
		}
	}
	harnesses := make([]string, 0, len(byHarness))
	for h := range byHarness {
		harnesses = append(harnesses, h)
	}
	sort.Strings(harnesses)
	restored := 0
	for _, h := range harnesses {
		b := byHarness[h]
		if b.hidden == "" || SessionSourceRank(b.hidden) <= SessionSourceRank(b.live) {
			continue
		}
		cond, args := sourceCondition([]string{b.hidden})
		res, err := tx.ExecContext(ctx, `UPDATE calls SET deleted_at = NULL,
				attributes = CASE WHEN json_valid(attributes) THEN json_remove(attributes, '$.weft.`+hiddenByKey+`') ELSE attributes END
			WHERE project_id = ? AND thread_id = ? AND deleted_at IS NOT NULL AND `+hiddenExpr+` IS NOT NULL AND `+cond,
			append([]any{pid, session}, args...)...)
		if err != nil {
			return restored, err
		}
		n, _ := res.RowsAffected()
		restored += int(n)
		if b.live != "" { // a lower source is shown: the restored one outranks it
			if _, err := s.retireLowerSources(ctx, tx, pid, session, b.hidden, sourcesRanked(b.hidden, false)); err != nil {
				return restored, err
			}
		}
	}
	if restored == 0 {
		return 0, nil
	}
	// Verdicts linked to spans of the session that are now hidden or
	// deleted link again (to the restored spans).
	ids, err := detQueryIDs(ctx, tx, `SELECT d.id FROM detections d JOIN calls c ON c.project_id = d.project_id AND c.id = d.span_id
		WHERE d.project_id = ? AND c.thread_id = ? AND c.deleted_at IS NOT NULL AND d.link <> 'replay'`, pid, session)
	if err != nil {
		return restored, err
	}
	for _, d := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE detections SET span_id = NULL, link = '' WHERE project_id = ? AND id = ?`, pid, d); err != nil {
			return restored, err
		}
		if err := linkDetectionTx(ctx, tx, pid, d); err != nil {
			return restored, err
		}
	}
	return restored, nil
}

// pluginClaimsIgnored reports a session whose plugin claims a project
// admin turned off (cached per write).
func (s *Store) pluginClaimsIgnored(ctx context.Context, tx *sql.Tx, pid, session string) (bool, error) {
	b, _ := ctx.Value(sourceBatchKey{}).(*sourceBatch)
	key := [2]string{pid, session}
	if b != nil {
		if v, ok := b.ignored[key]; ok {
			return v, nil
		}
	}
	var ignore bool
	err := tx.QueryRowContext(ctx, `SELECT ignore_plugin_claims FROM session_source_settings WHERE project_id = ? AND session_id = ?`,
		pid, session).Scan(&ignore)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if b != nil {
		b.ignored[key] = ignore
	}
	return ignore, nil
}

// SessionSourceInfo describes a session's ranked sources.
func (s *Store) SessionSourceInfo(ctx context.Context, projectID, session string) (*SessionSources, error) {
	pid, err := normalizeProjectID(projectID)
	if err != nil {
		return nil, err
	}
	if session == "" {
		return nil, Invalidf("session_id is required")
	}
	return sessionSourcesTx(ctx, s.r, pid, session)
}

func sessionSourcesTx(ctx context.Context, tx detDB, pid, session string) (*SessionSources, error) {
	counts, err := sessionSourceCounts(ctx, tx, pid, session)
	if err != nil {
		return nil, err
	}
	out := &SessionSources{SessionID: session, Sources: []SessionSourceCount{}}
	for _, c := range counts {
		out.Sources = append(out.Sources, *c)
	}
	sort.Slice(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		if a.Rank != b.Rank {
			return a.Rank > b.Rank
		}
		return a.Source < b.Source
	})
	err = tx.QueryRowContext(ctx, `SELECT ignore_plugin_claims FROM session_source_settings WHERE project_id = ? AND session_id = ?`,
		pid, session).Scan(&out.IgnorePluginClaims)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return out, nil
}

// RestoreSessionSources is a project admin's undo of plugin source claims
// for one session: with ignore set, the session's plugin spans lose their
// rank (their source moves to weft.claimed_source), later plugin claims
// for the session are not honoured, and its hidden spans are shown again.
// With ignore unset, later plugin claims are honoured again (spans already
// demoted stay so).
func (s *Store) RestoreSessionSources(ctx context.Context, projectID, session string, ignore bool, actor string) (*SessionSources, error) {
	pid, err := normalizeProjectID(projectID)
	if err != nil {
		return nil, err
	}
	if session == "" {
		return nil, Invalidf("session_id is required")
	}
	var out *SessionSources
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_source_settings(project_id, session_id, ignore_plugin_claims, updated_at, updated_by)
			VALUES(?,?,?,?,?) ON CONFLICT(project_id, session_id) DO UPDATE SET ignore_plugin_claims = excluded.ignore_plugin_claims,
			updated_at = excluded.updated_at, updated_by = excluded.updated_by`, pid, session, ignore, nowMicros(), actor); err != nil {
			return err
		}
		restored := 0
		if ignore {
			var plugins []string
			for _, src := range allRankedSources() {
				if IsPluginSource(src) {
					plugins = append(plugins, src)
				}
			}
			ph, args := sourceList(plugins)
			if _, err := tx.ExecContext(ctx, `UPDATE calls SET attributes = json_set(json_remove(attributes, '$.weft.source', '$.weft.`+hiddenByKey+`'),
					'$.weft.claimed_source', json_extract(attributes, '$.weft.source'))
				WHERE project_id = ? AND thread_id = ? AND json_valid(attributes) AND `+srcExpr+` IN (`+ph+`)`,
				append([]any{pid, session}, args...)...); err != nil {
				return err
			}
			if restored, err = s.restoreHiddenSources(ctx, tx, pid, session); err != nil {
				return err
			}
		}
		if out, err = sessionSourcesTx(ctx, tx, pid, session); err != nil {
			return err
		}
		out.Restored = restored
		return nil
	})
	return out, err
}

// restoreAfterDelete runs restoreHiddenSources for the sessions whose
// ranked-source spans a deletion removed.
func (s *Store) restoreAfterDelete(ctx context.Context, tx *sql.Tx, pid string, sessions []string) error {
	for _, sid := range sessions {
		n, err := s.restoreHiddenSources(ctx, tx, pid, sid)
		if err != nil {
			return err
		}
		if n > 0 {
			weftlog.From(ctx).Info("hidden spans shown again: the spans that outranked them were deleted", "project", pid, "session_id", sid, "spans", n)
		}
	}
	return nil
}
