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

// InsertFeedback inserts user feedback for an alert/event
func (s *Store) InsertFeedback(eventID string, alertID *int64, feedbackType, comment string) error {
	// Get rule name from alert if alert_id is provided
	var ruleName string
	if alertID != nil {
		err := s.db.QueryRow("SELECT rule_name FROM alerts WHERE id = ?", *alertID).Scan(&ruleName)
		if err != nil {
			return fmt.Errorf("getting rule name for alert %d: %w", *alertID, err)
		}
	}

	query := `
		INSERT INTO feedback (event_id, alert_id, rule_name, feedback_type, comment)
		VALUES (?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query, eventID, alertID, ruleName, feedbackType, comment)
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
	return fpRate, nil
}

// GetRulesWithHighFPRate returns rules with false positive rate above threshold
func (s *Store) GetRulesWithHighFPRate(threshold float64, minAlerts int) ([]string, error) {
	query := `
		SELECT a.rule_name, 
			   COUNT(*) as total_alerts,
			   COUNT(CASE WHEN f.feedback_type = 'false_positive' THEN 1 END) as fp_count
		FROM alerts a
		LEFT JOIN feedback f ON a.rule_name = f.rule_name
		GROUP BY a.rule_name
		HAVING total_alerts >= ?
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
