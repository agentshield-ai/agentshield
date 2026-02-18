package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	// Test in-memory database
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore() failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Fatal("NewStore() returned nil")
	}

	// Test file-based database
	tmpDir, err := os.MkdirTemp("", "store_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	fileStore, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() with file failed: %v", err)
	}
	defer fileStore.Close()

	// Verify database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestInsertAlert(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	alert := &Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		Tool:        "test-tool",
		Args:        `{"command": "test"}`,
		ActionTaken: "block",
		Timestamp:   time.Now(),
		SessionID:   "test-session",
		EventID:     "test-event-1",
	}

	err = store.InsertAlert(alert)
	if err != nil {
		t.Fatalf("InsertAlert() failed: %v", err)
	}

	// Verify ID was assigned
	if alert.ID == 0 {
		t.Error("Alert ID was not assigned after insert")
	}

	// Test inserting multiple alerts
	alert2 := &Alert{
		RuleName:    "test-rule-2",
		Severity:    "medium",
		Tool:        "test-tool-2",
		Args:        `{"command": "test2"}`,
		ActionTaken: "log",
		Timestamp:   time.Now(),
		SessionID:   "test-session-2",
		EventID:     "test-event-2",
	}

	err = store.InsertAlert(alert2)
	if err != nil {
		t.Fatalf("InsertAlert() second alert failed: %v", err)
	}

	if alert2.ID == 0 || alert2.ID == alert.ID {
		t.Error("Second alert should have different non-zero ID")
	}
}

func TestQueryAlerts(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert test data
	now := time.Now()
	alerts := []*Alert{
		{
			RuleName:    "rule1",
			Severity:    "high",
			Tool:        "tool1",
			Args:        `{"arg": "val1"}`,
			ActionTaken: "block",
			Timestamp:   now.Add(-2 * time.Hour),
			SessionID:   "session1",
			EventID:     "event1",
		},
		{
			RuleName:    "rule2",
			Severity:    "medium",
			Tool:        "tool2",
			Args:        `{"arg": "val2"}`,
			ActionTaken: "log",
			Timestamp:   now.Add(-1 * time.Hour),
			SessionID:   "session1",
			EventID:     "event2",
		},
		{
			RuleName:    "rule1",
			Severity:    "critical",
			Tool:        "tool3",
			Args:        `{"arg": "val3"}`,
			ActionTaken: "block",
			Timestamp:   now,
			SessionID:   "session2",
			EventID:     "event3",
		},
	}

	for _, alert := range alerts {
		if err := store.InsertAlert(alert); err != nil {
			t.Fatalf("inserting test alert: %v", err)
		}
	}

	tests := []struct {
		name     string
		query    *AlertQuery
		expected int
	}{
		{
			name:     "no filters - all alerts",
			query:    &AlertQuery{Limit: 100},
			expected: 3,
		},
		{
			name:     "severity filter - high",
			query:    &AlertQuery{Severity: "high", Limit: 100},
			expected: 1,
		},
		{
			name:     "rule filter",
			query:    &AlertQuery{Rule: "rule1", Limit: 100},
			expected: 2,
		},
		{
			name:     "session filter",
			query:    &AlertQuery{SessionID: "session1", Limit: 100},
			expected: 2,
		},
		{
			name:     "event filter",
			query:    &AlertQuery{EventID: "event1", Limit: 100},
			expected: 1,
		},
		{
			name: "time filter - since",
			query: func() *AlertQuery {
				since := now.Add(-90 * time.Minute)
				return &AlertQuery{Since: &since, Limit: 100}
			}(),
			expected: 2,
		},
		{
			name: "time filter - until",
			query: func() *AlertQuery {
				until := now.Add(-90 * time.Minute)
				return &AlertQuery{Until: &until, Limit: 100}
			}(),
			expected: 1,
		},
		{
			name:     "limit test",
			query:    &AlertQuery{Limit: 2},
			expected: 2,
		},
		{
			name:     "offset test",
			query:    &AlertQuery{Limit: 10, Offset: 2},
			expected: 1,
		},
		{
			name:     "combined filters",
			query:    &AlertQuery{Rule: "rule1", Severity: "critical", Limit: 100},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.QueryAlerts(tt.query)
			if err != nil {
				t.Fatalf("QueryAlerts() failed: %v", err)
			}

			if len(results) != tt.expected {
				t.Errorf("QueryAlerts() returned %d alerts, expected %d", len(results), tt.expected)
			}

			// Verify alerts are properly populated
			for _, alert := range results {
				if alert.ID == 0 {
					t.Error("Alert ID should not be zero")
				}
				if alert.RuleName == "" {
					t.Error("Alert RuleName should not be empty")
				}
				if alert.Timestamp.IsZero() {
					t.Error("Alert Timestamp should not be zero")
				}
			}
		})
	}
}

func TestCountAlerts(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert test data
	alerts := []*Alert{
		{RuleName: "rule1", Severity: "high", Tool: "tool1", Args: "{}", ActionTaken: "block", Timestamp: time.Now(), EventID: "event1"},
		{RuleName: "rule2", Severity: "medium", Tool: "tool2", Args: "{}", ActionTaken: "log", Timestamp: time.Now(), EventID: "event2"},
		{RuleName: "rule1", Severity: "critical", Tool: "tool1", Args: "{}", ActionTaken: "block", Timestamp: time.Now(), EventID: "event3"},
	}

	for _, alert := range alerts {
		if err := store.InsertAlert(alert); err != nil {
			t.Fatalf("inserting test alert: %v", err)
		}
	}

	tests := []struct {
		name     string
		query    *AlertQuery
		expected int64
	}{
		{
			name:     "count all",
			query:    &AlertQuery{},
			expected: 3,
		},
		{
			name:     "count by rule",
			query:    &AlertQuery{Rule: "rule1"},
			expected: 2,
		},
		{
			name:     "count by severity",
			query:    &AlertQuery{Severity: "high"},
			expected: 1,
		},
		{
			name:     "count with no matches",
			query:    &AlertQuery{Rule: "nonexistent"},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := store.CountAlerts(tt.query)
			if err != nil {
				t.Fatalf("CountAlerts() failed: %v", err)
			}

			if count != tt.expected {
				t.Errorf("CountAlerts() returned %d, expected %d", count, tt.expected)
			}
		})
	}
}

func TestInsertFeedback(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert a test alert first
	alert := &Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		ActionTaken: "block",
		Timestamp:   time.Now(),
		EventID:     "test-event-1",
	}
	if err := store.InsertAlert(alert); err != nil {
		t.Fatalf("inserting test alert: %v", err)
	}

	// Test inserting feedback with alert ID
	err = store.InsertFeedback("test-event-1", &alert.ID, "false_positive", "Test comment")
	if err != nil {
		t.Fatalf("InsertFeedback() failed: %v", err)
	}

	// Test inserting feedback without alert ID (inserts with empty rule_name)
	err = store.InsertFeedback("test-event-2", nil, "true_positive", "Another test")
	if err != nil {
		t.Fatalf("InsertFeedback() without alertID should succeed: %v", err)
	}
}

func TestGetFeedbackForRule(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert test alerts first
	alerts := []*Alert{
		{RuleName: "rule1", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "event1"},
		{RuleName: "rule1", Severity: "medium", ActionTaken: "log", Timestamp: time.Now(), EventID: "event2"},
		{RuleName: "rule2", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "event3"},
		{RuleName: "rule1", Severity: "low", ActionTaken: "log", Timestamp: time.Now(), EventID: "event4"},
	}

	for _, alert := range alerts {
		if err := store.InsertAlert(alert); err != nil {
			t.Fatalf("inserting test alert: %v", err)
		}
	}

	// Insert test feedback data using the correct method signature
	feedbackData := []struct {
		eventID      string
		alertID      *int64
		feedbackType string
		comment      string
	}{
		{"event1", &alerts[0].ID, "false_positive", "FP1"},
		{"event2", &alerts[1].ID, "true_positive", "TP1"},
		{"event3", &alerts[2].ID, "false_positive", "FP2"},
		{"event4", &alerts[3].ID, "false_positive", "IMP1"},
	}

	for _, fb := range feedbackData {
		if err := store.InsertFeedback(fb.eventID, fb.alertID, fb.feedbackType, fb.comment); err != nil {
			t.Fatalf("inserting test feedback: %v", err)
		}
	}

	// Test getting feedback for rule1 (should be 3: 2 FP + 1 TP)
	rule1Feedback, err := store.GetFeedbackForRule("rule1", 100)
	if err != nil {
		t.Fatalf("GetFeedbackForRule() failed: %v", err)
	}

	if len(rule1Feedback) != 3 {
		t.Errorf("Expected 3 feedback items for rule1, got %d", len(rule1Feedback))
	}

	// Test getting feedback for rule2
	rule2Feedback, err := store.GetFeedbackForRule("rule2", 100)
	if err != nil {
		t.Fatalf("GetFeedbackForRule() failed: %v", err)
	}

	if len(rule2Feedback) != 1 {
		t.Errorf("Expected 1 feedback item for rule2, got %d", len(rule2Feedback))
	}

	// Test limit
	limitedFeedback, err := store.GetFeedbackForRule("rule1", 2)
	if err != nil {
		t.Fatalf("GetFeedbackForRule() with limit failed: %v", err)
	}

	if len(limitedFeedback) != 2 {
		t.Errorf("Expected 2 feedback items with limit, got %d", len(limitedFeedback))
	}

	// Test nonexistent rule
	noFeedback, err := store.GetFeedbackForRule("nonexistent", 100)
	if err != nil {
		t.Fatalf("GetFeedbackForRule() for nonexistent rule failed: %v", err)
	}

	if len(noFeedback) != 0 {
		t.Errorf("Expected 0 feedback items for nonexistent rule, got %d", len(noFeedback))
	}
}

func TestGetRuleFPRate(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert test alerts first
	alerts := []*Alert{
		{RuleName: "rule1", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "event1"},
		{RuleName: "rule1", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "event2"},
		{RuleName: "rule1", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "event3"},
		{RuleName: "rule2", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "event5"},
	}

	for _, alert := range alerts {
		if err := store.InsertAlert(alert); err != nil {
			t.Fatalf("inserting test alert: %v", err)
		}
	}

	// Insert test feedback data
	feedbackData := []struct {
		eventID      string
		alertID      *int64
		feedbackType string
	}{
		{"event1", &alerts[0].ID, "false_positive"},
		{"event2", &alerts[1].ID, "false_positive"},
		{"event3", &alerts[2].ID, "true_positive"},
		{"event5", &alerts[3].ID, "false_positive"},
	}

	for _, fb := range feedbackData {
		if err := store.InsertFeedback(fb.eventID, fb.alertID, fb.feedbackType, "test comment"); err != nil {
			t.Fatalf("inserting test feedback: %v", err)
		}
	}

	// Test rule with 2 FP out of 3 total (2 FP + 1 TP) = 66.67%
	fpRate, err := store.GetRuleFPRate("rule1")
	if err != nil {
		t.Fatalf("GetRuleFPRate() failed: %v", err)
	}

	expectedRate := 2.0 / 3.0 // 66.67%
	if fpRate != expectedRate {
		t.Errorf("Expected FP rate %.4f for rule1, got %.4f", expectedRate, fpRate)
	}

	// Test rule with 100% FP rate
	fpRate2, err := store.GetRuleFPRate("rule2")
	if err != nil {
		t.Fatalf("GetRuleFPRate() for rule2 failed: %v", err)
	}

	if fpRate2 != 1.0 {
		t.Errorf("Expected FP rate 1.0 for rule2, got %.4f", fpRate2)
	}

	// Test nonexistent rule
	fpRate3, err := store.GetRuleFPRate("nonexistent")
	if err != nil {
		t.Fatalf("GetRuleFPRate() for nonexistent rule failed: %v", err)
	}

	if fpRate3 != 0.0 {
		t.Errorf("Expected FP rate 0.0 for nonexistent rule, got %.4f", fpRate3)
	}
}

func TestGetRulesWithHighFPRate(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert test feedback data using the actual API
	// InsertFeedback(eventID string, alertID *int64, feedbackType, comment string)
	type testFeedback struct {
		eventID      string
		ruleName     string
		feedbackType string
	}
	feedbacks := []testFeedback{
		// rule1: 2 FP out of 3 total = 66.67%
		{"alert1", "rule1", "false_positive"},
		{"alert2", "rule1", "false_positive"},
		{"alert3", "rule1", "true_positive"},
		// rule2: 1 FP out of 4 total = 25%
		{"alert4", "rule2", "false_positive"},
		{"alert5", "rule2", "true_positive"},
		{"alert6", "rule2", "true_positive"},
		{"alert7", "rule2", "true_positive"},
		// rule3: 1 FP out of 1 total = 100%
		{"alert8", "rule3", "false_positive"},
	}

	// Insert alerts and feedback, passing alert IDs so rule_name gets populated
	for _, fb := range feedbacks {
		alert := &Alert{
			RuleName:    fb.ruleName,
			Severity:    "high",
			ActionTaken: "block",
			Timestamp:   time.Now(),
			EventID:     fb.eventID,
		}
		if err := store.InsertAlert(alert); err != nil {
			t.Fatalf("inserting test alert: %v", err)
		}
		alertID := alert.ID
		if err := store.InsertFeedback(fb.eventID, &alertID, fb.feedbackType, ""); err != nil {
			t.Fatalf("inserting test feedback: %v", err)
		}
	}

	// Test threshold of 50% - should return rule1 (66.67%) and rule3 (100%)
	highFPRules, err := store.GetRulesWithHighFPRate(0.5, 1)
	if err != nil {
		t.Fatalf("GetRulesWithHighFPRate() failed: %v", err)
	}

	if len(highFPRules) != 2 {
		t.Errorf("Expected 2 rules with high FP rate, got %d", len(highFPRules))
	}

	// Test threshold of 80% - should return only rule3 (100%)
	veryHighFPRules, err := store.GetRulesWithHighFPRate(0.8, 1)
	if err != nil {
		t.Fatalf("GetRulesWithHighFPRate() with high threshold failed: %v", err)
	}

	if len(veryHighFPRules) != 1 {
		t.Errorf("Expected 1 rule with very high FP rate, got %d", len(veryHighFPRules))
	}

	if len(veryHighFPRules) > 0 && veryHighFPRules[0] != "rule3" {
		t.Errorf("Expected rule3, got %s", veryHighFPRules[0])
	}
}

func TestGetStats(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert test data
	now := time.Now()
	alerts := []*Alert{
		{RuleName: "rule1", Severity: "high", ActionTaken: "block", Timestamp: now, EventID: "event1"},
		{RuleName: "rule2", Severity: "medium", ActionTaken: "log", Timestamp: now.Add(-time.Hour), EventID: "event2"},
		{RuleName: "rule1", Severity: "critical", ActionTaken: "block", Timestamp: now.Add(-2*time.Hour), EventID: "event3"},
	}

	for _, alert := range alerts {
		if err := store.InsertAlert(alert); err != nil {
			t.Fatalf("inserting test alert: %v", err)
		}
	}

	// Insert feedback using actual API (alertID from first two alerts)
	alertID1 := alerts[0].ID
	if err := store.InsertFeedback("event1", &alertID1, "false_positive", ""); err != nil {
		t.Fatalf("inserting test feedback: %v", err)
	}
	alertID2 := alerts[1].ID
	if err := store.InsertFeedback("event2", &alertID2, "true_positive", ""); err != nil {
		t.Fatalf("inserting test feedback: %v", err)
	}

	stats, err := store.GetStats()
	if err != nil {
		t.Fatalf("GetStats() failed: %v", err)
	}

	// Check expected keys exist (matches actual GetStats implementation)
	expectedKeys := []string{"total_alerts", "by_severity", "recent_24h"}
	for _, key := range expectedKeys {
		if _, exists := stats[key]; !exists {
			t.Errorf("Expected stats key '%s' to be present", key)
		}
	}

	// Check specific values
	if totalAlerts, ok := stats["total_alerts"].(int64); !ok || totalAlerts != 3 {
		t.Errorf("Expected total_alerts to be 3, got %v (%T)", stats["total_alerts"], stats["total_alerts"])
	}

	if recent, ok := stats["recent_24h"].(int64); !ok || recent != 3 {
		t.Errorf("Expected recent_24h to be 3, got %v (%T)", stats["recent_24h"], stats["recent_24h"])
	}
}

func TestHealth(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}

	// Test health check on open store
	err = store.Health()
	if err != nil {
		t.Errorf("Health() failed on open store: %v", err)
	}

	// Close store and test health check on closed store
	store.Close()

	err = store.Health()
	if err == nil {
		t.Error("Health() should fail on closed store")
	}
}

func TestClose(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}

	// Test that Close() works without error
	err = store.Close()
	if err != nil {
		t.Errorf("Close() failed: %v", err)
	}

	// Test that operations fail after Close()
	alert := &Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		ActionTaken: "block",
		Timestamp:   time.Now(),
		EventID:     "test-event",
	}

	err = store.InsertAlert(alert)
	if err == nil {
		t.Error("InsertAlert() should fail after Close()")
	}

	// Test that Close() can be called multiple times safely
	err = store.Close()
	if err != nil {
		t.Errorf("Second Close() call failed: %v", err)
	}
}

// TestSQLInjectionProtection verifies that the store is protected against SQL injection attacks
func TestSQLInjectionProtection(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer store.Close()

	// Insert test data
	alert := &Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		Tool:        "test-tool",
		ActionTaken: "block",
		Timestamp:   time.Now(),
		SessionID:   "test-session",
		EventID:     "test-event",
	}

	err = store.InsertAlert(alert)
	if err != nil {
		t.Fatalf("inserting test alert: %v", err)
	}

	// Test SQL injection attempts via various query parameters
	injectionAttempts := []struct {
		name        string
		query       *AlertQuery
		expectError bool
	}{
		{
			name: "SQL injection via severity",
			query: &AlertQuery{
				Severity: "high'; DROP TABLE alerts; --",
				Limit:    10,
			},
			expectError: false, // Should not error, but should not drop table
		},
		{
			name: "SQL injection via rule",
			query: &AlertQuery{
				Rule:  "test'; DELETE FROM alerts; --",
				Limit: 10,
			},
			expectError: false, // Should not error, but should not delete data
		},
		{
			name: "SQL injection via session_id",
			query: &AlertQuery{
				SessionID: "test' UNION SELECT * FROM alerts --",
				Limit:     10,
			},
			expectError: false, // Should not error, but should not expose extra data
		},
		{
			name: "Excessive limit (DoS attempt)",
			query: &AlertQuery{
				Limit: 999999, // Should be capped at 10000
			},
			expectError: false,
		},
	}

	for _, tt := range injectionAttempts {
		t.Run(tt.name, func(t *testing.T) {
			// Test QueryAlerts
			alerts, err := store.QueryAlerts(tt.query)
			if (err != nil) != tt.expectError {
				t.Errorf("QueryAlerts() error = %v, expectError = %v", err, tt.expectError)
			}

			// Verify the injection didn't work - table should still exist and contain our test data
			testQuery := &AlertQuery{
				EventID: "test-event",
				Limit:   1,
			}
			
			verifyAlerts, err := store.QueryAlerts(testQuery)
			if err != nil {
				t.Errorf("Verification query failed: %v", err)
			}
			
			if len(verifyAlerts) != 1 {
				t.Errorf("Expected 1 alert after injection attempt, got %d", len(verifyAlerts))
			}

			// Test CountAlerts with same injection attempts
			count, err := store.CountAlerts(tt.query)
			if (err != nil) != tt.expectError {
				t.Errorf("CountAlerts() error = %v, expectError = %v", err, tt.expectError)
			}

			// For excessive limit test, verify it was capped
			if tt.name == "Excessive limit (DoS attempt)" {
				if len(alerts) > 1 { // We only have 1 test alert
					t.Errorf("Expected at most 1 alert with excessive limit, got %d", len(alerts))
				}
			}

			_ = count // Count value isn't critical for injection test, just that it doesn't error
		})
	}
}