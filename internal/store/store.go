package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// Alert represents an alert stored in the database
type Alert struct {
	ID          int64     `json:"id"`
	RuleName    string    `json:"rule_name"`
	Severity    string    `json:"severity"`
	Tool        string    `json:"tool"`
	Args        string    `json:"args"` // JSON string
	ActionTaken string    `json:"action_taken"`
	Timestamp   time.Time `json:"timestamp"`
	SessionID   string    `json:"session_id"`
	EventID     string    `json:"event_id"`
}

// AlertQuery represents query parameters for fetching alerts
type AlertQuery struct {
	Since     *time.Time
	Until     *time.Time
	Severity  string
	Rule      string
	SessionID string
	EventID   string
	Limit     int
	Offset    int
}

// Store manages the SQLite database for alerts and events
type Store struct {
	db   *sql.DB
	path string
}

// NewStore creates a new store instance and initializes the database
func NewStore(dbPath string) (*Store, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		// Note: In production, you might want to create the directory
		// but for security, we'll just validate it exists
	}

	// Open database connection
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Configure connection pool — SQLite supports only one writer at a time.
	// Using MaxOpenConns(1) serialises all writes to avoid SQLITE_BUSY errors.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	store := &Store{
		db:   db,
		path: dbPath,
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	return store, nil
}

// initSchema creates the database tables if they don't exist
func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		rule_name TEXT NOT NULL,
		severity TEXT NOT NULL,
		tool TEXT,
		args TEXT,
		action_taken TEXT NOT NULL,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		session_id TEXT,
		event_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_alerts_timestamp ON alerts(timestamp);
	CREATE INDEX IF NOT EXISTS idx_alerts_severity ON alerts(severity);
	CREATE INDEX IF NOT EXISTS idx_alerts_rule_name ON alerts(rule_name);
	CREATE INDEX IF NOT EXISTS idx_alerts_session_id ON alerts(session_id);
	CREATE INDEX IF NOT EXISTS idx_alerts_event_id ON alerts(event_id);

	-- Feedback table for storing user feedback on alerts
	CREATE TABLE IF NOT EXISTS feedback (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL,
		alert_id INTEGER,
		rule_name TEXT NOT NULL,
		feedback_type TEXT NOT NULL, -- 'false_positive', 'true_positive', 'false_negative'
		comment TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (alert_id) REFERENCES alerts(id)
	);

	CREATE INDEX IF NOT EXISTS idx_feedback_event_id ON feedback(event_id);
	CREATE INDEX IF NOT EXISTS idx_feedback_alert_id ON feedback(alert_id);
	CREATE INDEX IF NOT EXISTS idx_feedback_rule_name ON feedback(rule_name);
	CREATE INDEX IF NOT EXISTS idx_feedback_type ON feedback(feedback_type);
	`

	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}

	return nil
}

// InsertAlert inserts a new alert into the database
func (s *Store) InsertAlert(alert *Alert) error {
	query := `
		INSERT INTO alerts (rule_name, severity, tool, args, action_taken, timestamp, session_id, event_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	result, err := s.db.Exec(query,
		alert.RuleName,
		alert.Severity,
		alert.Tool,
		alert.Args,
		alert.ActionTaken,
		alert.Timestamp,
		alert.SessionID,
		alert.EventID,
	)

	if err != nil {
		return fmt.Errorf("inserting alert: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("getting inserted ID: %w", err)
	}

	alert.ID = id
	return nil
}

// buildWhereClause builds parameterized WHERE conditions from an AlertQuery.
// It returns the SQL clause (empty string if no conditions) and the bind args.
func buildWhereClause(query *AlertQuery) (string, []interface{}) {
	var conditions []string
	var args []interface{}

	if query.Since != nil {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, query.Since)
	}

	if query.Until != nil {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, query.Until)
	}

	if query.Severity != "" {
		conditions = append(conditions, "severity = ?")
		args = append(args, query.Severity)
	}

	if query.Rule != "" {
		conditions = append(conditions, "rule_name = ?")
		args = append(args, query.Rule)
	}

	if query.SessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, query.SessionID)
	}

	if query.EventID != "" {
		conditions = append(conditions, "event_id = ?")
		args = append(args, query.EventID)
	}

	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

// QueryAlerts queries alerts based on the provided criteria
func (s *Store) QueryAlerts(query *AlertQuery) ([]Alert, error) {
	whereClause, args := buildWhereClause(query)

	// Build SQL query with parameterized LIMIT/OFFSET
	query_sql := "SELECT id, rule_name, severity, tool, args, action_taken, timestamp, session_id, event_id FROM alerts" + whereClause

	query_sql += " ORDER BY timestamp DESC"

	// Validate and append LIMIT safely
	if query.Limit > 0 && query.Limit <= 10000 { // Add reasonable upper bound
		query_sql += " LIMIT ?"
		args = append(args, query.Limit)
	} else if query.Limit > 10000 {
		query_sql += " LIMIT ?"
		args = append(args, 10000) // Enforce maximum limit
	}

	// Validate and append OFFSET safely
	if query.Offset > 0 {
		query_sql += " OFFSET ?"
		args = append(args, query.Offset)
	}

	// Execute query
	rows, err := s.db.Query(query_sql, args...)
	if err != nil {
		return nil, fmt.Errorf("querying alerts: %w", err)
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var alert Alert
		var tool, args, sessionID, eventID sql.NullString

		err := rows.Scan(
			&alert.ID,
			&alert.RuleName,
			&alert.Severity,
			&tool,
			&args,
			&alert.ActionTaken,
			&alert.Timestamp,
			&sessionID,
			&eventID,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning alert row: %w", err)
		}

		// Handle nullable fields
		alert.Tool = tool.String
		alert.Args = args.String
		alert.SessionID = sessionID.String
		alert.EventID = eventID.String

		alerts = append(alerts, alert)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating alert rows: %w", err)
	}

	return alerts, nil
}

// CountAlerts returns the count of alerts matching the query criteria
func (s *Store) CountAlerts(query *AlertQuery) (int64, error) {
	whereClause, args := buildWhereClause(query)

	query_sql := "SELECT COUNT(*) FROM alerts" + whereClause

	var count int64
	err := s.db.QueryRow(query_sql, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting alerts: %w", err)
	}

	return count, nil
}

// Feedback represents user feedback stored in the database
type Feedback struct {
	ID           int64     `json:"id"`
	EventID      string    `json:"event_id"`
	AlertID      *int64    `json:"alert_id"`
	RuleName     string    `json:"rule_name"`
	FeedbackType string    `json:"feedback_type"`
	Comment      string    `json:"comment"`
	CreatedAt    time.Time `json:"created_at"`
}

// InsertFeedback inserts user feedback for an alert/event.
// If alertID is provided, ruleName is resolved from the alert row.
// If alertID is nil, caller-provided ruleName is used.
func (s *Store) InsertFeedback(eventID string, alertID *int64, ruleName, feedbackType, comment string) error {
	resolvedRuleName := ruleName
	if alertID != nil {
		err := s.db.QueryRow("SELECT rule_name FROM alerts WHERE id = ?", *alertID).Scan(&resolvedRuleName)
		if err != nil {
			return fmt.Errorf("getting rule name for alert %d: %w", *alertID, err)
		}
	}
	if resolvedRuleName == "" {
		return fmt.Errorf("rule_name is required")
	}

	query := `
		INSERT INTO feedback (event_id, alert_id, rule_name, feedback_type, comment)
		VALUES (?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query, eventID, alertID, resolvedRuleName, feedbackType, comment)
	if err != nil {
		return fmt.Errorf("inserting feedback: %w", err)
	}

	return nil
}

// GetFeedbackForRule retrieves feedback for a specific rule
func (s *Store) GetFeedbackForRule(ruleName string, limit int) ([]Feedback, error) {
	query := `
		SELECT id, event_id, alert_id, rule_name, feedback_type, comment, created_at
		FROM feedback 
		WHERE rule_name = ? 
		ORDER BY created_at DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, ruleName, limit)
	if err != nil {
		return nil, fmt.Errorf("querying feedback for rule %s: %w", ruleName, err)
	}
	defer rows.Close()

	var feedbacks []Feedback
	for rows.Next() {
		var feedback Feedback
		var alertID sql.NullInt64
		var comment sql.NullString

		err := rows.Scan(
			&feedback.ID,
			&feedback.EventID,
			&alertID,
			&feedback.RuleName,
			&feedback.FeedbackType,
			&comment,
			&feedback.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning feedback row: %w", err)
		}

		if alertID.Valid {
			feedback.AlertID = &alertID.Int64
		}
		feedback.Comment = comment.String

		feedbacks = append(feedbacks, feedback)
	}

	return feedbacks, nil
}

// GetRuleFPRate calculates the false positive rate for a rule based on feedback
func (s *Store) GetRuleFPRate(ruleName string) (float64, error) {
	// Count total alerts for this rule
	var totalAlerts int64
	err := s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE rule_name = ?", ruleName).Scan(&totalAlerts)
	if err != nil {
		return 0, fmt.Errorf("counting total alerts for rule %s: %w", ruleName, err)
	}

	if totalAlerts == 0 {
		return 0, nil
	}

	// Count false positive feedback
	var falsePositiveCount int64
	err = s.db.QueryRow(
		"SELECT COUNT(*) FROM feedback WHERE rule_name = ? AND feedback_type = 'false_positive'",
		ruleName,
	).Scan(&falsePositiveCount)
	if err != nil {
		return 0, fmt.Errorf("counting false positive feedback for rule %s: %w", ruleName, err)
	}

	// Calculate FP rate
	fpRate := float64(falsePositiveCount) / float64(totalAlerts)
	if fpRate > 1 {
		fpRate = 1
	}
	return fpRate, nil
}

// GetRulesWithHighFPRate returns rules with false positive rate above threshold
func (s *Store) GetRulesWithHighFPRate(threshold float64, minAlerts int) ([]string, error) {
	query := `
		WITH alert_counts AS (
			SELECT rule_name, COUNT(*) AS total_alerts
			FROM alerts
			GROUP BY rule_name
			HAVING COUNT(*) >= ?
		),
		fp_feedback AS (
			SELECT rule_name, COUNT(*) AS fp_count
			FROM feedback
			WHERE feedback_type = 'false_positive'
			GROUP BY rule_name
		)
		SELECT a.rule_name, a.total_alerts, COALESCE(f.fp_count, 0) AS fp_count
		FROM alert_counts a
		LEFT JOIN fp_feedback f ON a.rule_name = f.rule_name
		ORDER BY a.rule_name
	`

	rows, err := s.db.Query(query, minAlerts)
	if err != nil {
		return nil, fmt.Errorf("querying rules with high FP rate: %w", err)
	}
	defer rows.Close()

	var highFPRules []string
	for rows.Next() {
		var ruleName string
		var totalAlerts, fpCount int64

		err := rows.Scan(&ruleName, &totalAlerts, &fpCount)
		if err != nil {
			return nil, fmt.Errorf("scanning rule FP stats: %w", err)
		}

		// Calculate FP rate
		fpRate := float64(fpCount) / float64(totalAlerts)
		if fpRate > 1 {
			fpRate = 1
		}
		if fpRate >= threshold {
			highFPRules = append(highFPRules, ruleName)
		}
	}

	return highFPRules, nil
}

// GetStats returns database statistics
// EnforceRetention deletes alerts/feedback older than maxAgeDays.
// Returns number of alert rows deleted. maxAgeDays <= 0 disables cleanup.
func (s *Store) EnforceRetention(maxAgeDays int) (int64, error) {
	if maxAgeDays <= 0 {
		return 0, nil
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays)

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("starting retention transaction: %w", err)
	}
	defer tx.Rollback()

	// Keep feedback table bounded too.
	if _, err := tx.Exec("DELETE FROM feedback WHERE created_at < ?", cutoff); err != nil {
		return 0, fmt.Errorf("deleting old feedback: %w", err)
	}

	res, err := tx.Exec("DELETE FROM alerts WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("deleting old alerts: %w", err)
	}

	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting deleted row count: %w", err)
	}

	if _, err := tx.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return 0, fmt.Errorf("checkpointing sqlite wal: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing retention transaction: %w", err)
	}

	return deleted, nil
}

func (s *Store) GetStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Get total alert count
	var totalAlerts int64
	err := s.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&totalAlerts)
	if err != nil {
		return nil, fmt.Errorf("getting total alerts: %w", err)
	}
	stats["total_alerts"] = totalAlerts

	// Get alert count by severity
	rows, err := s.db.Query("SELECT severity, COUNT(*) FROM alerts GROUP BY severity")
	if err != nil {
		return nil, fmt.Errorf("getting severity stats: %w", err)
	}
	defer rows.Close()

	severityStats := make(map[string]int64)
	for rows.Next() {
		var severity string
		var count int64
		if err := rows.Scan(&severity, &count); err != nil {
			return nil, fmt.Errorf("scanning severity stats: %w", err)
		}
		severityStats[severity] = count
	}
	stats["by_severity"] = severityStats

	// Get recent activity (last 24 hours)
	var recentAlerts int64
	err = s.db.QueryRow("SELECT COUNT(*) FROM alerts WHERE timestamp >= datetime('now', '-1 day')").Scan(&recentAlerts)
	if err != nil {
		return nil, fmt.Errorf("getting recent alerts: %w", err)
	}
	stats["recent_24h"] = recentAlerts

	return stats, nil
}

// SessionSummary aggregates alert activity for a single session.
type SessionSummary struct {
	SessionID     string    `json:"session_id"`
	AlertCount    int64     `json:"alert_count"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	TopSeverity   string    `json:"top_severity"`
	DistinctRules int64     `json:"distinct_rules"`
	DistinctTools int64     `json:"distinct_tools"`
}

// RuleSummary aggregates alerts per rule.
type RuleSummary struct {
	RuleName   string    `json:"rule_name"`
	AlertCount int64     `json:"alert_count"`
	Severity   string    `json:"severity"`
	LastSeen   time.Time `json:"last_seen"`
	FPCount    int64     `json:"fp_count"`
	TPCount    int64     `json:"tp_count"`
}

// TimelineBucket is a single time-bucketed count.
type TimelineBucket struct {
	Bucket   time.Time `json:"bucket"`
	Total    int64     `json:"total"`
	Critical int64     `json:"critical"`
	High     int64     `json:"high"`
	Medium   int64     `json:"medium"`
	Low      int64     `json:"low"`
}

// GetAlertByID returns a single alert by primary key.
func (s *Store) GetAlertByID(id int64) (*Alert, error) {
	row := s.db.QueryRow(`
		SELECT id, rule_name, severity, tool, args, action_taken, timestamp, session_id, event_id
		FROM alerts WHERE id = ?`, id)

	var a Alert
	var tool, args, sessionID, eventID sql.NullString
	if err := row.Scan(&a.ID, &a.RuleName, &a.Severity, &tool, &args, &a.ActionTaken,
		&a.Timestamp, &sessionID, &eventID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying alert by id: %w", err)
	}
	a.Tool = tool.String
	a.Args = args.String
	a.SessionID = sessionID.String
	a.EventID = eventID.String
	return &a, nil
}

// GetSessions returns recent session summaries, ordered by most recent activity.
func (s *Store) GetSessions(limit int, since *time.Time) ([]SessionSummary, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	args := []interface{}{}
	where := "WHERE session_id IS NOT NULL AND session_id != ''"
	if since != nil {
		where += " AND timestamp >= ?"
		args = append(args, since)
	}
	q := `
		SELECT session_id,
			COUNT(*) AS alert_count,
			MIN(timestamp) AS first_seen,
			MAX(timestamp) AS last_seen,
			COUNT(DISTINCT rule_name) AS distinct_rules,
			COUNT(DISTINCT tool) AS distinct_tools,
			COALESCE(
				MAX(CASE severity
					WHEN 'critical' THEN 4
					WHEN 'high' THEN 3
					WHEN 'medium' THEN 2
					WHEN 'low' THEN 1
					ELSE 0
				END), 0) AS top_sev_rank
		FROM alerts ` + where + `
		GROUP BY session_id
		ORDER BY last_seen DESC
		LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying sessions: %w", err)
	}
	defer rows.Close()
	rankToSev := map[int]string{4: "critical", 3: "high", 2: "medium", 1: "low", 0: ""}
	out := []SessionSummary{}
	for rows.Next() {
		var sess SessionSummary
		var rank int
		if err := rows.Scan(&sess.SessionID, &sess.AlertCount, &sess.FirstSeen, &sess.LastSeen,
			&sess.DistinctRules, &sess.DistinctTools, &rank); err != nil {
			return nil, fmt.Errorf("scanning session row: %w", err)
		}
		sess.TopSeverity = rankToSev[rank]
		out = append(out, sess)
	}
	return out, rows.Err()
}

// GetRuleSummaries returns alert counts per rule with feedback counts.
func (s *Store) GetRuleSummaries(limit int) ([]RuleSummary, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `
		SELECT a.rule_name,
			COUNT(*) AS alert_count,
			MAX(a.timestamp) AS last_seen,
			-- severity is rule-level; pick any (MAX) as representative
			MAX(a.severity) AS severity,
			COALESCE(SUM(CASE WHEN f.feedback_type = 'false_positive' THEN 1 ELSE 0 END), 0) AS fp_count,
			COALESCE(SUM(CASE WHEN f.feedback_type = 'true_positive' THEN 1 ELSE 0 END), 0) AS tp_count
		FROM alerts a
		LEFT JOIN feedback f ON f.rule_name = a.rule_name
		GROUP BY a.rule_name
		ORDER BY alert_count DESC
		LIMIT ?`
	rows, err := s.db.Query(q, limit)
	if err != nil {
		return nil, fmt.Errorf("querying rule summaries: %w", err)
	}
	defer rows.Close()
	out := []RuleSummary{}
	for rows.Next() {
		var r RuleSummary
		if err := rows.Scan(&r.RuleName, &r.AlertCount, &r.LastSeen, &r.Severity, &r.FPCount, &r.TPCount); err != nil {
			return nil, fmt.Errorf("scanning rule summary row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTimeline returns time-bucketed alert counts over the last `hours` hours.
// bucketMinutes controls bucket granularity (e.g. 60 = hourly buckets).
func (s *Store) GetTimeline(hours, bucketMinutes int) ([]TimelineBucket, error) {
	if hours <= 0 || hours > 24*30 {
		hours = 24
	}
	if bucketMinutes <= 0 || bucketMinutes > 1440 {
		bucketMinutes = 60
	}
	seconds := bucketMinutes * 60
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	q := `
		SELECT
			CAST(strftime('%s', timestamp) AS INTEGER) / ? * ? AS bucket_unix,
			COUNT(*),
			SUM(CASE WHEN severity = 'critical' THEN 1 ELSE 0 END),
			SUM(CASE WHEN severity = 'high' THEN 1 ELSE 0 END),
			SUM(CASE WHEN severity = 'medium' THEN 1 ELSE 0 END),
			SUM(CASE WHEN severity = 'low' THEN 1 ELSE 0 END)
		FROM alerts
		WHERE timestamp >= ?
		GROUP BY bucket_unix
		ORDER BY bucket_unix ASC`
	rows, err := s.db.Query(q, seconds, seconds, since)
	if err != nil {
		return nil, fmt.Errorf("querying timeline: %w", err)
	}
	defer rows.Close()
	out := []TimelineBucket{}
	for rows.Next() {
		var b TimelineBucket
		var unix int64
		if err := rows.Scan(&unix, &b.Total, &b.Critical, &b.High, &b.Medium, &b.Low); err != nil {
			return nil, fmt.Errorf("scanning timeline bucket: %w", err)
		}
		b.Bucket = time.Unix(unix, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetTopTools returns the most frequent tools in alerts.
func (s *Store) GetTopTools(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := s.db.Query(`
		SELECT COALESCE(NULLIF(tool, ''), '(unknown)'), COUNT(*) AS c
		FROM alerts
		GROUP BY tool
		ORDER BY c DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("querying top tools: %w", err)
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	for rows.Next() {
		var name string
		var count int64
		if err := rows.Scan(&name, &count); err != nil {
			return nil, fmt.Errorf("scanning top tools: %w", err)
		}
		out = append(out, map[string]interface{}{"tool": name, "count": count})
	}
	return out, rows.Err()
}

// GetSessionAlerts returns all alerts for a session in chronological order.
func (s *Store) GetSessionAlerts(sessionID string, limit int) ([]Alert, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.Query(`
		SELECT id, rule_name, severity, tool, args, action_taken, timestamp, session_id, event_id
		FROM alerts
		WHERE session_id = ?
		ORDER BY timestamp ASC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying session alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var tool, args, sid, eid sql.NullString
		if err := rows.Scan(&a.ID, &a.RuleName, &a.Severity, &tool, &args, &a.ActionTaken,
			&a.Timestamp, &sid, &eid); err != nil {
			return nil, fmt.Errorf("scanning session alert: %w", err)
		}
		a.Tool = tool.String
		a.Args = args.String
		a.SessionID = sid.String
		a.EventID = eid.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// Close closes the database connection
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Health checks if the database connection is healthy
func (s *Store) Health() error {
	return s.db.Ping()
}
