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
// events rather than spans (the OTLP log exports of Claude Code, Codex and
// Gemini CLI, POST /otel/v1/logs). Events of one group (a prompt and the
// work it caused) arrive in batches every few seconds, possibly out of
// order, possibly re-sent, and the process may die mid-group. Weft keeps
// every event it accepts, keyed so that a re-sent event is stored once, and
// rebuilds a group's spans from all of its events each time one of them
// arrives. Span ids are derived from the events' own ids, so a rebuild
// upserts the same spans: they are extended, never duplicated, and a span
// whose parent becomes known later moves under it (detachFromOldParent).
//
// A group is named by the producer (Claude Code: every event carries its
// prompt.id) or by time: an event that opens a group (Opens: a user prompt)
// names it, and an event with no GroupID belongs to the group opened last
// before it in its session (Codex and Gemini CLI events carry no usable
// prompt id). Such an event is stored even if no group is open yet, and
// moves when an opener that precedes it arrives later.
//
// A session can be linked under another (LinkChild: a Codex sub-agent runs
// in a session of its own, started by a tool call of its parent): its
// events are then stored, and rebuilt, as part of the parent session, in
// the group open at their time. Spans built for the child session before
// the link arrived are retired.

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
CREATE TABLE IF NOT EXISTS log_session_links (
	project_id TEXT NOT NULL,
	source TEXT NOT NULL,
	child TEXT NOT NULL,
	parent TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, source, child)
);
`)
	registerMigration(func(db *sql.DB) error {
		ok, err := HasColumn(db, "log_events", "grouping")
		if err != nil {
			return err
		}
		if !ok {
			if _, err := db.Exec(`ALTER TABLE log_events ADD COLUMN grouping INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS log_events_open ON log_events(project_id, source, session_id, grouping, ts)`)
		return err
	})
}

// log_events.grouping: how an event's group was chosen.
const (
	groupNamed  = 0 // by the producer (LogEvent.GroupID)
	groupOpener = 1 // the event opens its group
	groupByTime = 2 // the group open at the event's time ("" while none is)
)

// LogEvent is one event of an event log.
type LogEvent struct {
	// Key identifies the event; a re-sent event has the same key.
	Key string
	// SessionID and GroupID place the event; spans are rebuilt per group.
	// An empty GroupID places the event in the group open at its time.
	SessionID string
	GroupID   string
	// Opens marks an event that opens the group GroupID (a user prompt).
	Opens bool
	// LinkChild names a session this event starts as part of its own
	// session (a sub-agent's): that session's events are stored here.
	LinkChild string
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

// LogSource describes one producer of event logs.
type LogSource struct {
	// Name keeps the producer's events apart (log_events.source).
	Name string
	// SpanSource is the weft.source of the spans Build returns; spans
	// built for a session that is later linked under another are retired
	// by it.
	SpanSource string
	Build      LogGroupBuilder
}

// IngestLogEvents stores the events of one source and project (events seen
// before are ignored), then rebuilds the spans of every group that received
// a new event, and of the group before it in its session (whose end a new
// group may settle), with src.Build, and stores them. It returns the calls
// that ended and how many events were new.
func (s *Store) IngestLogEvents(ctx context.Context, projectID string, src LogSource, events []LogEvent) ([]EndedCall, int, error) {
	pid, err := normalizeProjectID(projectID)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, 0, nil
	}
	source := src.Name
	// Serialise per session: a group is loaded and rebuilt under the lock,
	// so two batches of the same group cannot store a stale rebuild last.
	// The sessions linked sessions belong to are locked too.
	unlock := s.lockLogSessions(pid, source, s.logLockSessions(ctx, pid, source, events))
	defer unlock()

	touched := map[logGroupKey]bool{}
	var groups []LogGroup
	added := 0
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		w := &logWriter{ctx: ctx, tx: tx, pid: pid, src: src, touched: touched, parents: map[string]string{}}
		// Links first, so a sub-agent's events in the same batch land in
		// its parent's session; then openers, so the events after them
		// find their group.
		ordered := make([]LogEvent, 0, len(events))
		for pass := 0; pass < 3; pass++ {
			for _, e := range events {
				p := 2
				switch {
				case e.LinkChild != "":
					p = 0
				case e.Opens:
					p = 1
				}
				if p == pass {
					ordered = append(ordered, e)
				}
			}
		}
		for _, e := range ordered {
			if e.Key == "" || e.SessionID == "" || (e.GroupID == "" && e.Opens) {
				continue
			}
			if e.LinkChild != "" {
				if err := w.link(e.LinkChild, e.SessionID); err != nil {
					return err
				}
			}
			n, err := w.insert(e)
			if err != nil {
				return err
			}
			added += n
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
				if touched[logGroupKey{sid, g}] {
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
		for _, c := range src.Build(g) {
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

type logGroupKey struct{ session, group string }

// logWriter stores the events of one batch.
type logWriter struct {
	ctx     context.Context
	tx      *sql.Tx
	pid     string
	src     LogSource
	touched map[logGroupKey]bool
	parents map[string]string // session -> the session it is stored in
}

// home is the session a session's events are stored in.
func (w *logWriter) home(session string) (string, error) {
	if p, ok := w.parents[session]; ok {
		return p, nil
	}
	var parent string
	err := w.tx.QueryRowContext(w.ctx, `SELECT parent FROM log_session_links WHERE project_id = ? AND source = ? AND child = ?`,
		w.pid, w.src.Name, session).Scan(&parent)
	switch {
	case err == sql.ErrNoRows:
		parent = session
	case err != nil:
		return "", err
	}
	w.parents[session] = parent
	return parent, nil
}

// link stores child's events (stored and to come) in the session parent is
// stored in.
func (w *logWriter) link(child, parent string) error {
	home, err := w.home(parent)
	if err != nil {
		return err
	}
	cur, err := w.home(child)
	if err != nil || cur == home || child == home {
		return err
	}
	if cur != child {
		return nil // already linked elsewhere: the first link wins
	}
	now := nowMicros()
	if _, err := w.tx.ExecContext(w.ctx, `INSERT OR IGNORE INTO log_session_links(project_id, source, child, parent, created_at) VALUES(?,?,?,?,?)`,
		w.pid, w.src.Name, child, home, now); err != nil {
		return err
	}
	// Sessions linked under child move along.
	if _, err := w.tx.ExecContext(w.ctx, `UPDATE log_session_links SET parent = ? WHERE project_id = ? AND source = ? AND parent = ?`,
		home, w.pid, w.src.Name, child); err != nil {
		return err
	}
	for k, v := range w.parents {
		if v == child {
			w.parents[k] = home
		}
	}
	w.parents[child] = home
	// Events already stored under child join home, by time.
	rows, err := w.tx.QueryContext(w.ctx, `SELECT event_key, ts, seq FROM log_events WHERE project_id = ? AND source = ? AND session_id = ?`,
		w.pid, w.src.Name, child)
	if err != nil {
		return err
	}
	type ev struct {
		key     string
		ts, seq int64
	}
	var evs []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.key, &e.ts, &e.seq); err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range evs {
		g, err := w.groupAt(home, e.ts)
		if err != nil {
			return err
		}
		if _, err := w.tx.ExecContext(w.ctx, `UPDATE log_events SET session_id = ?, group_id = ?, grouping = ? WHERE project_id = ? AND source = ? AND event_key = ?`,
			home, g, groupByTime, w.pid, w.src.Name, e.key); err != nil {
			return err
		}
		if g != "" {
			w.touched[logGroupKey{home, g}] = true
		}
	}
	// Spans built for child as a session of its own go.
	if w.src.SpanSource == "" || len(evs) == 0 {
		return nil
	}
	_, err = w.tx.ExecContext(w.ctx, `UPDATE calls SET deleted_at = ? WHERE project_id = ? AND thread_id = ? AND deleted_at IS NULL
		AND json_extract(attributes, '$.weft.source') = ?`, now, w.pid, child, w.src.SpanSource)
	return err
}

// groupAt is the group open in session at us ("" if none).
func (w *logWriter) groupAt(session string, us int64) (string, error) {
	var g string
	err := w.tx.QueryRowContext(w.ctx, `SELECT group_id FROM log_events WHERE project_id = ? AND source = ? AND session_id = ? AND grouping = ? AND ts <= ?
		ORDER BY ts DESC, seq DESC, event_key DESC LIMIT 1`, w.pid, w.src.Name, session, groupOpener, us).Scan(&g)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return g, err
}

// insert stores one event and returns 1 if it is new.
func (w *logWriter) insert(e LogEvent) (int, error) {
	session, err := w.home(e.SessionID)
	if err != nil {
		return 0, err
	}
	group, grouping := e.GroupID, groupNamed
	switch {
	case session != e.SessionID: // a linked session's events are placed by time
		group, grouping = "", groupByTime
	case e.Opens:
		grouping = groupOpener
	case group == "":
		grouping = groupByTime
	}
	if grouping == groupByTime {
		if group, err = w.groupAt(session, e.TimeUS); err != nil {
			return 0, err
		}
	}
	res, err := w.tx.ExecContext(w.ctx, `INSERT OR IGNORE INTO log_events(project_id, source, event_key, session_id, group_id, grouping, name, ts, seq, attributes, received_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, w.pid, w.src.Name, e.Key, session, group, grouping, e.Name, e.TimeUS, e.Seq, mustJSON(e.Attrs), nowMicros())
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, nil
	}
	if group != "" {
		w.touched[logGroupKey{session, group}] = true
	}
	if grouping == groupOpener {
		// Events placed by time from here to the next opener are this group's.
		var next sql.NullInt64
		if err := w.tx.QueryRowContext(w.ctx, `SELECT MIN(ts) FROM log_events WHERE project_id = ? AND source = ? AND session_id = ? AND grouping = ? AND ts > ?`,
			w.pid, w.src.Name, session, groupOpener, e.TimeUS).Scan(&next); err != nil {
			return 0, err
		}
		const where = ` WHERE project_id = ? AND source = ? AND session_id = ? AND grouping = ? AND ts >= ? AND (? = 0 OR ts < ?) AND group_id <> ?`
		args := []any{w.pid, w.src.Name, session, groupByTime, e.TimeUS, boolInt(next.Valid), next.Int64, group}
		rows, err := w.tx.QueryContext(w.ctx, `SELECT DISTINCT group_id FROM log_events`+where, args...)
		if err != nil {
			return 0, err
		}
		var old []string
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err != nil {
				rows.Close()
				return 0, err
			}
			old = append(old, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		if len(old) > 0 {
			if _, err := w.tx.ExecContext(w.ctx, `UPDATE log_events SET group_id = ?`+where, append([]any{group}, args...)...); err != nil {
				return 0, err
			}
			for _, g := range old {
				if g != "" {
					w.touched[logGroupKey{session, g}] = true
				}
			}
		}
	}
	return 1, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
	rows, err := tx.QueryContext(ctx, `SELECT group_id FROM log_events WHERE project_id = ? AND source = ? AND session_id = ? AND group_id <> ''
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
	rows, err := tx.QueryContext(ctx, `SELECT event_key, name, ts, seq, grouping, attributes FROM log_events
		WHERE project_id = ? AND source = ? AND session_id = ? AND group_id = ? ORDER BY ts, seq, event_key`, pid, source, session, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEvent
	for rows.Next() {
		e := LogEvent{SessionID: session, GroupID: group}
		var attrs string
		var grouping int
		if err := rows.Scan(&e.Key, &e.Name, &e.TimeUS, &e.Seq, &grouping, &attrs); err != nil {
			return nil, err
		}
		e.Opens = grouping == groupOpener
		e.Attrs = decodeMap(attrs)
		out = append(out, e)
	}
	return out, rows.Err()
}

// logLockSessions lists the sessions a batch writes to: its events'
// sessions, the sessions they are linked under, and sessions it links.
func (s *Store) logLockSessions(ctx context.Context, pid, source string, events []LogEvent) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, e := range events {
		add(e.SessionID)
		add(e.LinkChild)
	}
	if len(out) == 0 {
		return nil
	}
	c, a := inList("child", out)
	rows, err := s.r.QueryContext(ctx, `SELECT parent FROM log_session_links WHERE project_id = ? AND source = ? AND `+c, append([]any{pid, source}, a...)...)
	if err != nil {
		return out // the table may not exist yet on a read replica: lock what we know
	}
	defer rows.Close()
	var parents []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			parents = append(parents, p)
		}
	}
	for _, p := range parents {
		add(p)
	}
	return out
}

// lockLogSessions locks the stripes of sessions, in a fixed order, and
// returns the unlock function.
func (s *Store) lockLogSessions(pid, source string, sessions []string) func() {
	idx := map[int]bool{}
	for _, sid := range sessions {
		h := fnv.New32a()
		_, _ = h.Write([]byte(pid + "\x00" + source + "\x00" + sid))
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM log_session_links WHERE project_id = ? AND created_at < ?`, pid, us); err != nil &&
		!strings.Contains(err.Error(), "no such table") {
		return 0, err
	}
	return r.RowsAffected()
}
