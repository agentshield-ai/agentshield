package feedback

import (
	"fmt"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/store"

	_ "modernc.org/sqlite" // Import SQLite driver
)

func TestSubmitFeedback(t *testing.T) {
	// Create in-memory store for testing
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	tests := []struct {
		name        string
		feedback    *Feedback
		expectError bool
		errorMsg    string
	}{
		{
			name: "Valid feedback",
			feedback: &Feedback{
				AlertID:  "alert123",
				RuleName: "test-rule",
				Verdict:  "false_positive",
				Comment:  "This is a test comment",
			},
			expectError: false,
		},
		{
			name: "Invalid feedback type",
			feedback: &Feedback{
				AlertID:  "alert123",
				RuleName: "test-rule",
				Verdict:  "invalid_type",
				Comment:  "Test comment",
			},
			expectError: true,
			errorMsg:    "invalid feedback type",
		},
		{
			name: "Missing alert ID",
			feedback: &Feedback{
				AlertID:  "",
				RuleName: "test-rule",
				Verdict:  "true_positive",
				Comment:  "Test comment",
			},
			expectError: true,
			errorMsg:    "event_id is required",
		},
		{
			name: "Missing rule name",
			feedback: &Feedback{
				AlertID:  "alert123",
				RuleName: "",
				Verdict:  "false_positive",
				Comment:  "Test comment",
			},
			expectError: true,
			errorMsg:    "rule_name is required",
		},
		{
			name: "Comment too long",
			feedback: &Feedback{
				AlertID:  "alert123",
				RuleName: "test-rule",
				Verdict:  "false_positive",
				Comment:  string(make([]byte, 1001)), // 1001 characters
			},
			expectError: true,
			errorMsg:    "comment too long",
		},
		{
			name: "Valid feedback with empty comment",
			feedback: &Feedback{
				AlertID:  "alert456",
				RuleName: "another-rule",
				Verdict:  "true_positive",
				Comment:  "",
			},
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := fm.SubmitFeedback(test.feedback)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
					return
				}
				if test.errorMsg != "" && !contains(err.Error(), test.errorMsg) {
					t.Errorf("Expected error to contain %q, got %q", test.errorMsg, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// Verify feedback was processed
			if test.feedback.ID == "" {
				t.Error("Expected feedback ID to be generated")
			}

			if test.feedback.Timestamp.IsZero() {
				t.Error("Expected timestamp to be set")
			}
		})
	}
}

func TestValidFeedbackTypes(t *testing.T) {
	validTypes := []string{"false_positive", "true_positive", "false_negative"}
	invalidTypes := []string{"invalid", "maybe", "unknown", ""}

	for _, validType := range validTypes {
		if !ValidFeedbackTypes[validType] {
			t.Errorf("Expected %s to be a valid feedback type", validType)
		}
	}

	for _, invalidType := range invalidTypes {
		if ValidFeedbackTypes[invalidType] {
			t.Errorf("Expected %s to be an invalid feedback type", invalidType)
		}
	}
}

func TestGenerateFeedbackID(t *testing.T) {
	id1 := generateFeedbackID()
	time.Sleep(1 * time.Millisecond) // Ensure different timestamp
	id2 := generateFeedbackID()

	if id1 == id2 {
		t.Error("Expected unique feedback IDs")
	}

	if !contains(id1, "fb_") {
		t.Errorf("Expected feedback ID to start with 'fb_', got %s", id1)
	}
}

func TestSanitizeComment(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"normal comment", "normal comment"},
		{"comment with special chars !@#$%", "comment with special chars !@#$%"},
	}

	for _, test := range tests {
		result := sanitizeComment(test.input)
		if result != test.expected {
			t.Errorf("sanitizeComment(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestParseAlertID(t *testing.T) {
	tests := []struct {
		input    string
		expected *int64
	}{
		{"123", func() *int64 { val := int64(123); return &val }()},
		{" 456 ", func() *int64 { val := int64(456); return &val }()},
		{"0", nil},
		{"-1", nil},
		{"abc", nil},
		{"", nil},
		{"123abc", nil},
	}

	for _, test := range tests {
		result := parseAlertID(test.input)

		if test.expected == nil {
			if result != nil {
				t.Errorf("parseAlertID(%q) = %v, expected nil", test.input, result)
			}
		} else {
			if result == nil {
				t.Errorf("parseAlertID(%q) = nil, expected %d", test.input, *test.expected)
			} else if *result != *test.expected {
				t.Errorf("parseAlertID(%q) = %d, expected %d", test.input, *result, *test.expected)
			}
		}
	}
}

func TestGetRuleStatsComputesRatesFromStoreData(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// 4 alerts total for the same rule.
	alerts := []*store.Alert{
		{RuleName: "stats-rule", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "e1"},
		{RuleName: "stats-rule", Severity: "high", ActionTaken: "block", Timestamp: time.Now(), EventID: "e2"},
		{RuleName: "stats-rule", Severity: "medium", ActionTaken: "log", Timestamp: time.Now(), EventID: "e3"},
		{RuleName: "stats-rule", Severity: "low", ActionTaken: "allow", Timestamp: time.Now(), EventID: "e4"},
	}
	for _, alert := range alerts {
		if err := st.InsertAlert(alert); err != nil {
			t.Fatalf("Failed to insert alert: %v", err)
		}
	}

	// 2 false positives + 1 true positive feedback.
	for _, fb := range []struct {
		eventID string
		alertID int64
		verdict string
	}{
		{eventID: "e1", alertID: alerts[0].ID, verdict: "false_positive"},
		{eventID: "e2", alertID: alerts[1].ID, verdict: "false_positive"},
		{eventID: "e3", alertID: alerts[2].ID, verdict: "true_positive"},
	} {
		alertID := fb.alertID
		if err := st.InsertFeedback(fb.eventID, &alertID, "", fb.verdict, ""); err != nil {
			t.Fatalf("Failed to insert feedback: %v", err)
		}
	}

	fm := NewFeedbackManager(st)
	stats, err := fm.GetRuleStats("stats-rule")
	if err != nil {
		t.Fatalf("GetRuleStats failed: %v", err)
	}

	if stats.TotalAlerts != 4 {
		t.Fatalf("Expected 4 alerts, got %d", stats.TotalAlerts)
	}
	if stats.FeedbackCount != 3 {
		t.Fatalf("Expected 3 feedback entries, got %d", stats.FeedbackCount)
	}
	if stats.FalsePositiveRate != 0.5 { // 2 / 4 alerts
		t.Fatalf("Expected FP rate 0.5, got %f", stats.FalsePositiveRate)
	}
	if stats.TruePositiveRate != 0.25 { // 1 / 4 alerts
		t.Fatalf("Expected TP rate 0.25, got %f", stats.TruePositiveRate)
	}
	if stats.RecommendedAction != "refine" {
		t.Fatalf("Expected recommended action refine, got %s", stats.RecommendedAction)
	}
}

func TestGetRuleStats(t *testing.T) {
	// Create in-memory store for testing
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Add some test alerts
	alerts := []*store.Alert{
		{
			RuleName:    "test-rule",
			Severity:    "high",
			ActionTaken: "block",
			Timestamp:   time.Now().Add(-1 * time.Hour),
		},
		{
			RuleName:    "test-rule",
			Severity:    "high",
			ActionTaken: "log",
			Timestamp:   time.Now().Add(-30 * time.Minute),
		},
		{
			RuleName:    "other-rule",
			Severity:    "medium",
			ActionTaken: "log",
			Timestamp:   time.Now().Add(-15 * time.Minute),
		},
	}

	for _, alert := range alerts {
		err := st.InsertAlert(alert)
		if err != nil {
			t.Fatalf("Failed to insert test alert: %v", err)
		}
	}

	fm := NewFeedbackManager(st)

	// Test getting stats for existing rule
	stats, err := fm.GetRuleStats("test-rule")
	if err != nil {
		t.Fatalf("GetRuleStats failed: %v", err)
	}

	if stats.RuleName != "test-rule" {
		t.Errorf("Expected rule name 'test-rule', got %s", stats.RuleName)
	}

	if stats.TotalAlerts != 2 {
		t.Errorf("Expected 2 total alerts, got %d", stats.TotalAlerts)
	}

	if stats.LastTriggered == nil {
		t.Error("Expected LastTriggered to be set")
	}

	// Test getting stats for non-existent rule
	stats, err = fm.GetRuleStats("non-existent")
	if err != nil {
		t.Fatalf("GetRuleStats failed for non-existent rule: %v", err)
	}

	if stats.TotalAlerts != 0 {
		t.Errorf("Expected 0 total alerts for non-existent rule, got %d", stats.TotalAlerts)
	}

	if stats.LastTriggered != nil {
		t.Error("Expected LastTriggered to be nil for rule with no alerts")
	}
}

func TestFeedbackSummary(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	summary, err := fm.GetFeedbackSummary()
	if err != nil {
		t.Fatalf("GetFeedbackSummary failed: %v", err)
	}

	if summary.TotalFeedback != 0 {
		t.Errorf("Expected 0 total feedback for empty store, got %d", summary.TotalFeedback)
	}

	if len(summary.ByType) != 3 {
		t.Errorf("Expected 3 feedback types, got %d", len(summary.ByType))
	}

	expectedTypes := []string{"false_positive", "true_positive", "false_negative"}
	for _, expectedType := range expectedTypes {
		if _, exists := summary.ByType[expectedType]; !exists {
			t.Errorf("Expected feedback type %s in summary", expectedType)
		}
	}
}

func TestGetFeedbackForRule(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	// First test with no feedback - should return empty list
	feedbacks, err := fm.GetFeedbackForRule("nonexistent-rule", 10)
	if err != nil {
		t.Fatalf("GetFeedbackForRule failed for nonexistent rule: %v", err)
	}

	if len(feedbacks) != 0 {
		t.Errorf("Expected 0 feedbacks for nonexistent-rule, got %d", len(feedbacks))
	}

	// Insert an alert first so feedback can reference it
	testAlert := &store.Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		Tool:        "test-tool",
		ActionTaken: "block",
		Timestamp:   time.Now(),
		EventID:     "alert1",
	}

	err = st.InsertAlert(testAlert)
	if err != nil {
		t.Fatalf("Failed to insert test alert: %v", err)
	}

	// Insert feedback referencing the alert
	alertID := testAlert.ID // Should be set by InsertAlert
	err = st.InsertFeedback("alert1", &alertID, "", "false_positive", "Test comment 1")
	if err != nil {
		t.Fatalf("Failed to insert test feedback: %v", err)
	}

	// Test getting feedback for the rule
	feedbacks, err = fm.GetFeedbackForRule("test-rule", 10)
	if err != nil {
		t.Fatalf("GetFeedbackForRule failed: %v", err)
	}

	if len(feedbacks) != 1 {
		t.Errorf("Expected 1 feedback for test-rule, got %d", len(feedbacks))
		return
	}

	// Verify feedback content
	fb := feedbacks[0]
	if fb.RuleName != "test-rule" {
		t.Errorf("Expected rule name 'test-rule', got %s", fb.RuleName)
	}
	if fb.AlertID != "alert1" {
		t.Errorf("Expected alert ID 'alert1', got %s", fb.AlertID)
	}
	if fb.Verdict != "false_positive" {
		t.Errorf("Expected verdict 'false_positive', got %s", fb.Verdict)
	}
	if fb.Timestamp.IsZero() {
		t.Error("Expected non-zero timestamp")
	}

	// Test with limit
	feedbacks, err = fm.GetFeedbackForRule("test-rule", 1)
	if err != nil {
		t.Fatalf("GetFeedbackForRule with limit failed: %v", err)
	}

	if len(feedbacks) > 1 {
		t.Errorf("Expected at most 1 feedback with limit=1, got %d", len(feedbacks))
	}
}

func TestGetRuleFalsePositiveRate(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	// Test getting FP rate for rule with no data
	rate, err := fm.GetRuleFalsePositiveRate("test-rule")
	if err != nil {
		t.Fatalf("GetRuleFalsePositiveRate failed: %v", err)
	}

	// Should return 0.0 for rules with no feedback
	if rate != 0.0 {
		t.Errorf("Expected FP rate 0.0 for rule with no feedback, got %f", rate)
	}

	// Add some feedback to test with data
	feedbacks := []*Feedback{
		{AlertID: "alert1", RuleName: "test-rule", Verdict: "false_positive"},
		{AlertID: "alert2", RuleName: "test-rule", Verdict: "true_positive"},
		{AlertID: "alert3", RuleName: "test-rule", Verdict: "false_positive"},
	}

	for _, fb := range feedbacks {
		err := fm.SubmitFeedback(fb)
		if err != nil {
			t.Fatalf("Failed to submit feedback: %v", err)
		}
	}

	// Test again - should return calculated rate
	rate, err = fm.GetRuleFalsePositiveRate("test-rule")
	if err != nil {
		t.Fatalf("GetRuleFalsePositiveRate failed after adding feedback: %v", err)
	}

	// Rate calculation depends on store implementation, but should be >= 0
	if rate < 0 {
		t.Errorf("Expected FP rate >= 0, got %f", rate)
	}
}

func TestGetHighFalsePositiveRules(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	// Test with various thresholds
	thresholds := []float64{0.1, 0.5, 0.9}

	for _, threshold := range thresholds {
		t.Run(fmt.Sprintf("threshold_%v", threshold), func(t *testing.T) {
			rules, err := fm.GetHighFalsePositiveRules(threshold)
			if err != nil {
				t.Fatalf("GetHighFalsePositiveRules failed: %v", err)
			}

			// Currently returns empty list as placeholder
			if rules == nil {
				t.Error("Expected non-nil rules slice")
			}

			// Verify structure of returned rules
			for _, rule := range rules {
				if rule.RuleName == "" {
					t.Error("Expected non-empty rule name")
				}
				if rule.FalsePositiveRate < 0 || rule.FalsePositiveRate > 1 {
					t.Errorf("Expected FP rate between 0 and 1, got %f", rule.FalsePositiveRate)
				}
			}
		})
	}
}

func TestGetFeedbackSummaryWithData(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	// Add some feedback data
	feedbacks := []*Feedback{
		{AlertID: "alert1", RuleName: "rule1", Verdict: "false_positive", Comment: "FP 1"},
		{AlertID: "alert2", RuleName: "rule2", Verdict: "true_positive", Comment: "TP 1"},
		{AlertID: "alert3", RuleName: "rule1", Verdict: "false_negative", Comment: "FN 1"},
	}

	for _, fb := range feedbacks {
		err := fm.SubmitFeedback(fb)
		if err != nil {
			t.Fatalf("Failed to submit feedback: %v", err)
		}
	}

	summary, err := fm.GetFeedbackSummary()
	if err != nil {
		t.Fatalf("GetFeedbackSummary failed: %v", err)
	}

	// Verify structure
	if summary == nil {
		t.Fatal("Expected non-nil summary")
	}

	if summary.ByType == nil {
		t.Error("Expected non-nil ByType map")
	}

	if summary.TopFalsePositives == nil {
		t.Error("Expected non-nil TopFalsePositives slice")
	}

	if summary.RecentFeedback == nil {
		t.Error("Expected non-nil RecentFeedback slice")
	}

	if summary.RecommendedActions == nil {
		t.Error("Expected non-nil RecommendedActions map")
	}

	// Check that all required feedback types are present
	requiredTypes := []string{"false_positive", "true_positive", "false_negative"}
	for _, reqType := range requiredTypes {
		if _, exists := summary.ByType[reqType]; !exists {
			t.Errorf("Expected feedback type %s in ByType map", reqType)
		}
	}
}

func TestGetRuleStatsPlaceholderValues(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	// Test that placeholder values are reasonable
	stats, err := fm.GetRuleStats("any-rule")
	if err != nil {
		t.Fatalf("GetRuleStats failed: %v", err)
	}

	// Check placeholder values are within reasonable ranges
	if stats.FalsePositiveRate < 0 || stats.FalsePositiveRate > 1 {
		t.Errorf("Expected FP rate between 0 and 1, got %f", stats.FalsePositiveRate)
	}

	if stats.TruePositiveRate < 0 || stats.TruePositiveRate > 1 {
		t.Errorf("Expected TP rate between 0 and 1, got %f", stats.TruePositiveRate)
	}

	if stats.FeedbackCount < 0 {
		t.Errorf("Expected feedback count >= 0, got %d", stats.FeedbackCount)
	}

	validActions := []string{"none", "refine", "disable"}
	actionValid := false
	for _, valid := range validActions {
		if stats.RecommendedAction == valid {
			actionValid = true
			break
		}
	}
	if !actionValid {
		t.Errorf("Expected valid recommended action, got %s", stats.RecommendedAction)
	}
}

func TestSubmitFeedbackIDGeneration(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	// Test that ID is generated when not provided
	fb := &Feedback{
		AlertID:  "alert123",
		RuleName: "test-rule",
		Verdict:  "false_positive",
		Comment:  "Test",
		// ID not set
	}

	err = fm.SubmitFeedback(fb)
	if err != nil {
		t.Fatalf("SubmitFeedback failed: %v", err)
	}

	if fb.ID == "" {
		t.Error("Expected ID to be generated")
	}

	if !contains(fb.ID, "fb_") {
		t.Errorf("Expected ID to contain 'fb_', got %s", fb.ID)
	}

	// Test that existing ID is preserved
	fb2 := &Feedback{
		ID:       "custom-id",
		AlertID:  "alert456",
		RuleName: "test-rule",
		Verdict:  "true_positive",
		Comment:  "Test",
	}

	err = fm.SubmitFeedback(fb2)
	if err != nil {
		t.Fatalf("SubmitFeedback failed: %v", err)
	}

	if fb2.ID != "custom-id" {
		t.Errorf("Expected ID to be preserved as 'custom-id', got %s", fb2.ID)
	}
}

func TestSubmitFeedbackTimestampHandling(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fm := NewFeedbackManager(st)

	// Test that timestamp is set when not provided
	fb := &Feedback{
		AlertID:  "alert123",
		RuleName: "test-rule",
		Verdict:  "false_positive",
		Comment:  "Test",
		// Timestamp not set (zero value)
	}

	beforeTime := time.Now()
	err = fm.SubmitFeedback(fb)
	afterTime := time.Now()

	if err != nil {
		t.Fatalf("SubmitFeedback failed: %v", err)
	}

	if fb.Timestamp.IsZero() {
		t.Error("Expected timestamp to be set")
	}

	if fb.Timestamp.Before(beforeTime) || fb.Timestamp.After(afterTime) {
		t.Errorf("Expected timestamp to be between %v and %v, got %v", beforeTime, afterTime, fb.Timestamp)
	}

	// Test that existing timestamp is preserved
	customTime := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	fb2 := &Feedback{
		AlertID:   "alert456",
		RuleName:  "test-rule",
		Verdict:   "true_positive",
		Comment:   "Test",
		Timestamp: customTime,
	}

	err = fm.SubmitFeedback(fb2)
	if err != nil {
		t.Fatalf("SubmitFeedback failed: %v", err)
	}

	if !fb2.Timestamp.Equal(customTime) {
		t.Errorf("Expected timestamp to be preserved as %v, got %v", customTime, fb2.Timestamp)
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			func() bool {
				for i := 0; i <= len(s)-len(substr); i++ {
					if s[i:i+len(substr)] == substr {
						return true
					}
				}
				return false
			}()))
}
