// Package feedback provides feedback collection and rule refinement.
// This handles user feedback, rule tuning, and continuous improvement.
package feedback

import (
	"fmt"
	"html"
	"time"

	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/google/uuid"
)

// Feedback represents user feedback on an alert or rule
type Feedback struct {
	ID        string    `json:"id"`
	AlertID   string    `json:"alert_id"`
	RuleName  string    `json:"rule_name"`
	Verdict   string    `json:"verdict"` // "false_positive", "true_positive", "false_negative"
	Comment   string    `json:"comment,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ValidFeedbackTypes defines the allowed feedback types
var ValidFeedbackTypes = map[string]bool{
	"false_positive": true,
	"true_positive":  true,
	"false_negative": true,
}

// FeedbackManager handles feedback operations
type FeedbackManager struct {
	store *store.Store
}

// NewFeedbackManager creates a new feedback manager
func NewFeedbackManager(store *store.Store) *FeedbackManager {
	return &FeedbackManager{
		store: store,
	}
}

// SubmitFeedback validates and stores feedback
func (fm *FeedbackManager) SubmitFeedback(feedback *Feedback) error {
	// Validate feedback type
	if !ValidFeedbackTypes[feedback.Verdict] {
		return fmt.Errorf("invalid feedback type: %s", feedback.Verdict)
	}

	// Validate required fields
	if feedback.AlertID == "" {
		return fmt.Errorf("alert_id is required")
	}

	if feedback.RuleName == "" {
		return fmt.Errorf("rule_name is required")
	}

	// Validate comment length
	const maxCommentLength = 1000
	if len(feedback.Comment) > maxCommentLength {
		return fmt.Errorf("comment too long (max %d characters)", maxCommentLength)
	}

	// Sanitize comment (basic HTML/script tag removal)
	feedback.Comment = sanitizeComment(feedback.Comment)

	// Generate ID if not provided
	if feedback.ID == "" {
		feedback.ID = generateFeedbackID()
	}

	// Set timestamp if not provided
	if feedback.Timestamp.IsZero() {
		feedback.Timestamp = time.Now()
	}

	// Store in database using the existing store method
	alertID := parseAlertID(feedback.AlertID)
	return fm.store.InsertFeedback(feedback.AlertID, alertID, feedback.Verdict, feedback.Comment)
}

// GetFeedbackForRule retrieves feedback for a specific rule
func (fm *FeedbackManager) GetFeedbackForRule(ruleName string, limit int) ([]Feedback, error) {
	// Use the store's GetFeedbackForRule method
	storeFeedbacks, err := fm.store.GetFeedbackForRule(ruleName, limit)
	if err != nil {
		return nil, fmt.Errorf("querying feedback for rule %s: %w", ruleName, err)
	}

	// Convert store feedback to feedback package feedback
	var feedbacks []Feedback
	for _, sf := range storeFeedbacks {
		fb := Feedback{
			ID:        fmt.Sprintf("%d", sf.ID),
			AlertID:   sf.EventID,
			RuleName:  sf.RuleName,
			Verdict:   sf.FeedbackType,
			Comment:   sf.Comment,
			Timestamp: sf.CreatedAt,
		}
		feedbacks = append(feedbacks, fb)
	}

	return feedbacks, nil
}

// GetRuleFalsePositiveRate calculates the false positive rate for a rule
func (fm *FeedbackManager) GetRuleFalsePositiveRate(ruleName string) (float64, error) {
	// Use the store's GetRuleFPRate method
	return fm.store.GetRuleFPRate(ruleName)
}

// RuleStats represents statistics about a rule's performance
type RuleStats struct {
	RuleName           string  `json:"rule_name"`
	TotalAlerts        int     `json:"total_alerts"`
	FalsePositiveRate  float64 `json:"false_positive_rate"`
	TruePositiveRate   float64 `json:"true_positive_rate"`
	FeedbackCount      int     `json:"feedback_count"`
	LastTriggered      *time.Time `json:"last_triggered"`
	RecommendedAction  string  `json:"recommended_action"`
}

// GetRuleStats returns comprehensive statistics for a rule
func (fm *FeedbackManager) GetRuleStats(ruleName string) (*RuleStats, error) {
	alertQuery := &store.AlertQuery{
		Rule:  ruleName,
		Limit: 1000,
	}

	alerts, err := fm.store.QueryAlerts(alertQuery)
	if err != nil {
		return nil, fmt.Errorf("querying alerts for rule %s: %w", ruleName, err)
	}

	stats := &RuleStats{
		RuleName:          ruleName,
		TotalAlerts:       len(alerts),
		FalsePositiveRate: 0.1, // TODO: compute from store.GetRuleFPRate()
		TruePositiveRate:  0.8, // TODO: compute from feedback data
		FeedbackCount:     0,   // TODO: count from feedback table
		RecommendedAction: "none",
	}

	// Set last triggered if we have alerts
	if len(alerts) > 0 {
		// Alerts are ordered by timestamp DESC, so first is most recent
		stats.LastTriggered = &alerts[0].Timestamp
	}

	// Determine recommended action based on FP rate
	if stats.FalsePositiveRate > 0.5 {
		stats.RecommendedAction = "disable"
	} else if stats.FalsePositiveRate > 0.3 {
		stats.RecommendedAction = "refine"
	}

	return stats, nil
}

// GetHighFalsePositiveRules returns rules with high false positive rates
func (fm *FeedbackManager) GetHighFalsePositiveRules(threshold float64) ([]RuleStats, error) {
	// TODO: implement using store.GetRulesWithHighFPRate(threshold)
	return []RuleStats{}, nil
}

// Utility functions

func sanitizeComment(comment string) string {
	if comment == "" {
		return ""
	}

	// Use html.EscapeString from stdlib which encodes all five HTML special
	// characters (<, >, &, ', ") preventing stored XSS via attribute injection
	// and double-encoding edge cases that manual replacement can miss.
	return html.EscapeString(comment)
}

func generateFeedbackID() string {
	// Generate a unique UUID-based ID
	return "fb_" + uuid.New().String()
}

func parseAlertID(alertIDStr string) *int64 {
	// Try to parse alert ID as int64
	// This is a simplified implementation
	var alertID int64
	n, err := fmt.Sscanf(alertIDStr, "%d", &alertID)
	if err == nil && n == 1 {
		// Check if the entire string was consumed (no extra characters)
		expectedStr := fmt.Sprintf("%d", alertID)
		if alertIDStr == expectedStr {
			return &alertID
		}
	}
	return nil
}

// FeedbackSummary provides an overview of feedback for reporting
type FeedbackSummary struct {
	TotalFeedback      int                    `json:"total_feedback"`
	ByType             map[string]int         `json:"by_type"`
	TopFalsePositives  []string              `json:"top_false_positives"`
	RecentFeedback     []Feedback            `json:"recent_feedback"`
	RecommendedActions map[string]string     `json:"recommended_actions"`
}

// GetFeedbackSummary returns a summary of all feedback
func (fm *FeedbackManager) GetFeedbackSummary() (*FeedbackSummary, error) {
	// This would require comprehensive feedback queries
	// Placeholder implementation
	summary := &FeedbackSummary{
		TotalFeedback: 0,
		ByType: map[string]int{
			"false_positive": 0,
			"true_positive":  0,
			"false_negative": 0,
		},
		TopFalsePositives:  []string{},
		RecentFeedback:     []Feedback{},
		RecommendedActions: map[string]string{},
	}

	return summary, nil
}