package store

// Session text search (GET /api/v1/sessions?q=, docs/weft/API.md).
//
// Besides a session's id, name, title (first user request), team and
// agents, `q` matches:
//
//   - reasoning step text (reasoning_steps.text);
//   - message text: the user and assistant messages of model spans and the
//     input and output text of agent and turn spans;
//   - tool calls: the tool name and its arguments, for executed tools and
//     for tool calls a model requested.
//
// Message and tool text is extracted once, at ingest, into span_search as
// plain text (at most searchTextCap characters per span and kind), so a
// search reads short rows instead of decoding every span's JSON. SQLite FTS5
// is not available in both of Weft's drivers (the encrypted-database driver
// is built without it), so matching is a case-insensitive substring match
// (LIKE) over the project's rows; a query is limited to maxSearchQuery
// characters and a search to maxSearchSessions sessions.

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"
)

const (
	searchTextCap     = 8000
	maxSearchQuery    = 200
	maxSearchSessions = 5000

	searchKindMessage = "message"
	searchKindTool    = "tool"
)

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS span_search (
	project_id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	text TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, span_id, kind)
);
`)
	registerMigration(backfillSpanSearch)
	registerCallHook(indexSpanSearch)
}

// spanSearchText extracts the searchable message and tool text of a span.
func spanSearchText(op string, isTurn bool, toolName string, inputs map[string]any, output any) (message, tool string) {
	var msg, tl []string
	switch op {
	case OpExecuteTool:
		tl = append(tl, toolName, InputText(inputs))
	case OpChat:
		for _, m := range append(InputMessages(inputs), OutputMessages(output)...) {
			if m.Role == "user" || m.Role == "assistant" {
				if t := messageContent(m); t != "" {
					msg = append(msg, t)
				}
			}
			for _, tc := range m.ToolCalls {
				tl = append(tl, tc.Name+" "+tc.Arguments)
			}
		}
	}
	if op == OpInvokeAgent || isTurn {
		msg = append(msg, inputPreview(inputs), outputPreview(output))
	}
	return capText(msg), capText(tl)
}

func capText(parts []string) string {
	kept := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	s := strings.Join(kept, "\n")
	if len(s) > searchTextCap {
		s = s[:searchTextCap]
		for i := 0; i < utf8.UTFMax && !utf8.ValidString(s); i++ { // do not split a character
			s = s[:len(s)-1]
		}
	}
	return s
}

// indexSpanSearch (a call hook) refreshes a span's search text.
func indexSpanSearch(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	var op, toolName, inputs string
	var isTurn bool
	var output sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT s.operation_name, s.is_turn, s.tool_name, c.inputs, c.output
		FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		WHERE s.project_id = ? AND s.id = ?`, projectID, id).Scan(&op, &isTurn, &toolName, &inputs, &output)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var message, tool string
	if op == OpChat || op == OpExecuteTool || op == OpInvokeAgent || isTurn {
		var out any
		if output.Valid {
			out = decodeJSON(output.String)
		}
		message, tool = spanSearchText(op, isTurn, toolName, decodeMap(inputs), out)
	}
	for _, kt := range [][2]string{{searchKindMessage, message}, {searchKindTool, tool}} {
		if kt[1] == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM span_search WHERE project_id = ? AND span_id = ? AND kind = ?`, projectID, id, kt[0]); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO span_search(project_id, span_id, kind, text) VALUES(?,?,?,?)
			ON CONFLICT(project_id, span_id, kind) DO UPDATE SET text = excluded.text`, projectID, id, kt[0], kt[1]); err != nil {
			return err
		}
	}
	return nil
}

const spanSearchBackfill = "span_search_v1"

// backfillSpanSearch indexes spans stored before the search index existed,
// once per database.
func backfillSpanSearch(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM weft_backfills WHERE name = ?`, spanSearchBackfill).Scan(&n); err != nil || n > 0 {
		return err
	}
	rows, err := db.Query(`SELECT s.project_id, s.id FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		WHERE c.deleted_at IS NULL AND (s.operation_name IN ('chat', 'execute_tool', 'invoke_agent') OR s.is_turn = 1)`)
	if err != nil {
		return err
	}
	var keys [][2]string
	for rows.Next() {
		var p, id string
		if err := rows.Scan(&p, &id); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, [2]string{p, id})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, k := range keys {
		if err := indexSpanSearch(ctx, tx, k[0], k[1]); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO weft_backfills(name, done_at) VALUES(?, ?)`, spanSearchBackfill, nowMicros()); err != nil {
		return err
	}
	return tx.Commit()
}

// likePattern is a LIKE pattern matching q anywhere (escape character \).
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// normSearchQuery trims q and caps its length.
func normSearchQuery(q string) string {
	q = strings.TrimSpace(q)
	if r := []rune(q); len(r) > maxSearchQuery {
		q = string(r[:maxSearchQuery])
	}
	return q
}

// sessionsMatchingText returns the sessions whose reasoning, messages or
// tool calls contain q (case-insensitive).
func (s *Store) sessionsMatchingText(ctx context.Context, pid, q string) (map[string]bool, error) {
	out := map[string]bool{}
	q = normSearchQuery(q)
	if q == "" {
		return out, nil
	}
	pat := likePattern(q)
	rows, err := s.r.QueryContext(ctx, `SELECT a.conversation_id FROM span_search t
			JOIN agent_spans a ON a.project_id = t.project_id AND a.id = t.span_id
			JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
		WHERE t.project_id = ? AND a.conversation_id <> '' AND c.deleted_at IS NULL AND t.text LIKE ? ESCAPE '\'
		UNION
		SELECT r.session_id FROM reasoning_steps r JOIN calls c ON c.project_id = r.project_id AND c.id = r.span_id
		WHERE r.project_id = ? AND r.session_id <> '' AND c.deleted_at IS NULL AND r.text LIKE ? ESCAPE '\'
		LIMIT ?`, pid, pat, pid, pat, maxSearchSessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
