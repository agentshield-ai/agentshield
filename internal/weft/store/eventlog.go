package store

import (
	"context"
	"database/sql"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Event logs: producers that report what an agent does as a stream of log
// events rather than spans (Claude Code's OTLP log export, POST
// /otel/v1/logs). Events of one group (Claude Code: one prompt) arrive in
// batches every few seconds, possibly out of order, possibly re-sent, and
// the process may die mid-group. Weft keeps every event it accepts, keyed so
// that a re-sent event is stored once, and rebuilds a group's spans from all
// of its events each time one of them arrives. Span ids are derived from the
// events' own ids, so a rebuild upserts the same spans: they are extended,
// never duplicated, and a span whose parent becomes known later moves under
// it (detachFromOldParent).

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS log_events (
	project_id TEXT NOT NULL,
	source TEXT NOT NULL,
	event_key TEXT NOT NULL,
	session_id TEXT NOT NULL,
	group_id TEXT NOT NULL,
	name TEXT NOT NULL,
	ts INTEGER NOT NULL,
	seq INTEGER NOT NULL DEFAULT 0,
	attributes TEXT NOT NULL DEFAULT '{}',
	received_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, source, event_key)
);
CREATE INDEX IF NOT EXISTS log_events_group ON log_events(project_id, source, session_id, group_id);
CREATE INDEX IF NOT EXISTS log_events_ts ON log_events(project_id, ts);
`)
}

// LogEvent is one event of an event log.
type LogEvent struct {
	// Key identifies the event; a re-sent event has the same key.
	Key string
	// SessionID and GroupID place the event; spans are rebuilt per group.
	SessionID string
	GroupID   string
	Name      string
	// TimeUS is the event time (unix microseconds); Seq orders events
	// with the same time.
	TimeUS int64
	Seq    int64
	Attrs  map[string]any
}

// LogGroup is what a builder gets to rebuild one group's spans.
type LogGroup struct {
	SessionID, GroupID string
	// Events are all stored events of the group, in time order.
	Events []LogEvent
	// Later reports that the session has a group that started after this one.
	Later bool
	// First holds the events of the session's earliest group (this
	// group's own events when it is the earliest).
	First []LogEvent
}

// LogGroupBuilder turns a group's events into spans. It must be
// deterministic: the same events give the same spans.
type LogGroupBuilder func(g LogGroup) []CompleteCall

// IngestLogEvents stores the events of one source and project (events seen
// before are ignored), then rebuilds the spans of every group that received
// a new event, and of the group before it in its session (whose end a new
// group may settle), with build, and stores them. It returns the calls that
// ended and how many events were new.
func (s *Store) IngestLogEvents(ctx context.Context, projectID, source string, events []LogEvent, build LogGroupBuilder) ([]EndedCall, int, error) {
	pid, err := normalizeProjectID(projectID)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, 0, nil
	}
	// Serialise per session: a group is loaded and rebuilt under the lock,
	// so two batches of the same group cannot store a stale rebuild last.
	unlock := s.lockLogSessions(pid, source, events)
	defer unlock()

	type gk struct{ session, group string }
	touched := map[gk]bool{}
	var groups []LogGroup
	added := 0
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		now := nowMicros()
		for _, e := range events {
			if e.Key == "" || e.SessionID == "" || e.GroupID == "" {
				continue
			}
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO log_events(project_id, source, event_key, session_id, group_id, name, ts, seq, attributes, received_at)
				VALUES(?,?,?,?,?,?,?,?,?,?)`, pid, source, e.Key, e.SessionID, e.GroupID, e.Name, e.TimeUS, e.Seq, mustJSON(e.Attrs), now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
				touched[gk{e.SessionID, e.GroupID}] = true
			}
		}
		sessions := map[string]bool{}
		for k := range touched {
			sessions[k.session] = true
		}
		names := make([]string, 0, len(sessions))
		for sid := range sessions {
			names = append(names, sid)
		}
		sort.Strings(names)
		for _, sid := range names {
			order, err := logGroupOrder(ctx, tx, pid, source, sid)
			if err != nil {
				return err
			}
			rebuild := map[int]bool{}
			for i, g := range order {
				if touched[gk{sid, g}] {
					rebuild[i] = true
					if i > 0 {
						rebuild[i-1] = true
					}
				}
			}
			var first []LogEvent
			for i, g := range order {
				if !rebuild[i] {
					continue
				}
				evs, err := loadLogGroup(ctx, tx, pid, source, sid, g)
				if err != nil {
					return err
				}
				if i == 0 {
					first = evs
				} else if first == nil {
					if first, err = loadLogGroup(ctx, tx, pid, source, sid, order[0]); err != nil {
						return err
					}
				}
				groups = append(groups, LogGroup{SessionID: sid, GroupID: g, Events: evs, Later: i < len(order)-1, First: first})
			}
		}
		return nil
	})
	if err != nil || len(groups) == 0 {
		return nil, added, err
	}
	var calls []CompleteCall
	for _, g := range groups {
		for _, c := range build(g) {
			c.Start.ProjectID = pid
			calls = append(calls, c)
		}
	}
	if len(calls) == 0 {
		return nil, added, nil
	}
	ended, err := s.UpsertComplete(ctx, calls)
	return ended, added, err
}

func normalizeProjectID(p string) (string, error) {
	pid, err := refs.NormalizeProject(p)
	if err != nil {
		return "", &InvalidError{Msg: err.Error()}
	}
	return pid, nil
}

// logGroupOrder lists a session's groups by their first event.
func logGroupOrder(ctx context.Context, tx *sql.Tx, pid, source, session string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_id FROM log_events WHERE project_id = ? AND source = ? AND session_id = ?
		GROUP BY group_id ORDER BY MIN(ts), group_id`, pid, source, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func loadLogGroup(ctx context.Context, tx *sql.Tx, pid, source, session, group string) ([]LogEvent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_key, name, ts, seq, attributes FROM log_events
		WHERE project_id = ? AND source = ? AND session_id = ? AND group_id = ? ORDER BY ts, seq, event_key`, pid, source, session, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEvent
	for rows.Next() {
		e := LogEvent{SessionID: session, GroupID: group}
		var attrs string
		if err := rows.Scan(&e.Key, &e.Name, &e.TimeUS, &e.Seq, &attrs); err != nil {
			return nil, err
		}
		e.Attrs = decodeMap(attrs)
		out = append(out, e)
	}
	return out, rows.Err()
}

// lockLogSessions locks the stripes of the sessions events belong to, in a
// fixed order, and returns the unlock function.
func (s *Store) lockLogSessions(pid, source string, events []LogEvent) func() {
	idx := map[int]bool{}
	for _, e := range events {
		h := fnv.New32a()
		_, _ = h.Write([]byte(pid + "\x00" + source + "\x00" + e.SessionID))
		idx[int(h.Sum32()%uint32(len(s.logLocks)))] = true
	}
	order := make([]int, 0, len(idx))
	for i := range idx {
		order = append(order, i)
	}
	sort.Ints(order)
	for _, i := range order {
		s.logLocks[i].Lock()
	}
	return func() {
		for j := len(order) - 1; j >= 0; j-- {
			s.logLocks[order[j]].Unlock()
		}
	}
}

// purgeLogEventsBefore removes a project's events older than us.
func purgeLogEventsBefore(ctx context.Context, tx *sql.Tx, pid string, us int64) (int64, error) {
	r, err := tx.ExecContext(ctx, `DELETE FROM log_events WHERE project_id = ? AND ts < ?`, pid, us)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return r.RowsAffected()
}
