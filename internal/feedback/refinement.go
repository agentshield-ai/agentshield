package feedback

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/agentshield-ai/agentshield/internal/triage"
	"gopkg.in/yaml.v3"
)

// RefinementSuggestion represents a suggested rule improvement
type RefinementSuggestion struct {
	Type        string `json:"type"`        // "add_exception", "narrow_condition", "reduce_severity", "disable"
	Description string `json:"description"` // Human-readable description
	Before      string `json:"before"`      // Current rule content (relevant section)
	After       string `json:"after"`       // Suggested rule content
	Confidence  float64 `json:"confidence"` // 0.0-1.0 confidence in suggestion
	Reasoning   string `json:"reasoning"`   // Why this change is suggested
}

// RuleRefinementResult contains analysis and suggestions for a rule
type RuleRefinementResult struct {
	RuleName           string                 `json:"rule_name"`
	RuleFile           string                 `json:"rule_file"`
	FalsePositiveRate  float64                `json:"false_positive_rate"`
	TotalAlerts        int                    `json:"total_alerts"`
	FeedbackCount      int                    `json:"feedback_count"`
	CommonPatterns     []string               `json:"common_patterns"`
	Suggestions        []RefinementSuggestion `json:"suggestions"`
	LLMAnalysis        *triage.TriageResult  `json:"llm_analysis,omitempty"`
	RecommendedAction  string                `json:"recommended_action"`
}

// RefinementEngine handles rule analysis and improvement suggestions
type RefinementEngine struct {
	feedbackManager *FeedbackManager
	rulesDir        string
	triager         *triage.Triager
}

// NewRefinementEngine creates a new refinement engine
func NewRefinementEngine(feedbackManager *FeedbackManager, rulesDir string, triager *triage.Triager) *RefinementEngine {
	return &RefinementEngine{
		feedbackManager: feedbackManager,
		rulesDir:        rulesDir,
		triager:         triager,
	}
}

// AnalyzeRule performs comprehensive analysis of a rule and suggests improvements
func (re *RefinementEngine) AnalyzeRule(ctx context.Context, ruleName string) (*RuleRefinementResult, error) {
	// Get rule statistics
	stats, err := re.feedbackManager.GetRuleStats(ruleName)
	if err != nil {
		return nil, fmt.Errorf("getting rule stats: %w", err)
	}

	// Find the rule file
	ruleFile, err := re.findRuleFile(ruleName)
	if err != nil {
		return nil, fmt.Errorf("finding rule file: %w", err)
	}

	// Analyze false positive patterns
	patterns, err := re.analyzeFalsePositivePatterns(ruleName)
	if err != nil {
		return nil, fmt.Errorf("analyzing patterns: %w", err)
	}

	// Generate suggestions based on statistics and patterns
	suggestions := re.generateSuggestions(stats, patterns)

	// Determine recommended action
	recommendedAction := re.determineRecommendedAction(stats.FalsePositiveRate)

	result := &RuleRefinementResult{
		RuleName:          ruleName,
		RuleFile:          ruleFile,
		FalsePositiveRate: stats.FalsePositiveRate,
		TotalAlerts:       stats.TotalAlerts,
		FeedbackCount:     stats.FeedbackCount,
		CommonPatterns:    patterns,
		Suggestions:       suggestions,
		RecommendedAction: recommendedAction,
	}

	// If triage is enabled and FP rate is high, get LLM analysis
	if re.triager != nil && stats.FalsePositiveRate > 0.2 && stats.TotalAlerts > 10 {
		llmAnalysis, err := re.getLLMRefinementSuggestion(ctx, result)
		if err == nil {
			result.LLMAnalysis = llmAnalysis
		}
		// Don't fail if LLM analysis fails
	}

	return result, nil
}

// GetRulesNeedingRefinement returns rules with high false positive rates
func (re *RefinementEngine) GetRulesNeedingRefinement(threshold float64) ([]RuleRefinementResult, error) {
	rules, err := re.feedbackManager.GetHighFalsePositiveRules(threshold)
	if err != nil {
		return nil, err
	}

	var results []RuleRefinementResult
	for _, rule := range rules {
		if rule.FalsePositiveRate >= threshold {
			// Create basic result - full analysis would be expensive for many rules
			result := RuleRefinementResult{
				RuleName:          rule.RuleName,
				FalsePositiveRate: rule.FalsePositiveRate,
				TotalAlerts:       rule.TotalAlerts,
				FeedbackCount:     rule.FeedbackCount,
				RecommendedAction: rule.RecommendedAction,
			}

			ruleFile, err := re.findRuleFile(rule.RuleName)
			if err == nil {
				result.RuleFile = ruleFile
			}

			results = append(results, result)
		}
	}

	// Sort by false positive rate (highest first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].FalsePositiveRate > results[j].FalsePositiveRate
	})

	return results, nil
}

// ApplyRefinement applies a suggested refinement to a rule file
func (re *RefinementEngine) ApplyRefinement(ruleName string, suggestion *RefinementSuggestion, backup bool) error {
	ruleFile, err := re.findRuleFile(ruleName)
	if err != nil {
		return fmt.Errorf("finding rule file: %w", err)
	}

	// SECURITY: Validate file path BEFORE any file I/O to prevent directory
	// traversal attacks from reading or writing files outside the rules directory.
	if err := re.validateRuleFilePath(ruleFile); err != nil {
		return fmt.Errorf("invalid rule file path: %w", err)
	}

	// Read current rule file
	content, err := os.ReadFile(ruleFile)
	if err != nil {
		return fmt.Errorf("reading rule file: %w", err)
	}

	// Create backup if requested
	if backup {
		backupFile := fmt.Sprintf("%s.backup.%d", ruleFile, time.Now().Unix())
		// Validate backup path is also within rules dir
		if err := re.validateRuleFilePath(backupFile); err != nil {
			return fmt.Errorf("invalid backup file path: %w", err)
		}
		if err := os.WriteFile(backupFile, content, 0600); err != nil {
			return fmt.Errorf("creating backup: %w", err)
		}
	}

	// Apply the refinement based on type
	newContent, err := re.applyRefinementToContent(string(content), suggestion)
	if err != nil {
		return fmt.Errorf("applying refinement: %w", err)
	}

	// Validate the new YAML
	if err := re.validateRuleYAML(newContent); err != nil {
		return fmt.Errorf("validating refined rule: %w", err)
	}

	// Write the refined rule
	if err := os.WriteFile(ruleFile, []byte(newContent), 0600); err != nil {
		return fmt.Errorf("writing refined rule: %w", err)
	}

	return nil
}

// Private helper methods

func (re *RefinementEngine) findRuleFile(ruleName string) (string, error) {
	var ruleFile string
	
	err := filepath.WalkDir(re.rulesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		
		if !d.IsDir() && (strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")) {
			// Read and check if this file contains the rule
			content, err := os.ReadFile(path)
			if err != nil {
				return nil // Skip files we can't read
			}
			
			// Parse YAML to check rule name
			var rule struct {
				Name string `yaml:"name"`
				ID   string `yaml:"id"`
			}
			
			if yaml.Unmarshal(content, &rule) == nil {
				if rule.Name == ruleName || rule.ID == ruleName {
					ruleFile = path
					return filepath.SkipAll // Found it, stop walking
				}
			}
		}
		
		return nil
	})
	
	if err != nil {
		return "", err
	}
	
	if ruleFile == "" {
		return "", fmt.Errorf("rule file not found for rule: %s", ruleName)
	}
	
	return ruleFile, nil
}

func (re *RefinementEngine) analyzeFalsePositivePatterns(ruleName string) ([]string, error) {
	// Query alerts that were marked as false positives
	alertQuery := &store.AlertQuery{
		Rule:  ruleName,
		Limit: 100, // Sample size
	}
	
	alerts, err := re.feedbackManager.store.QueryAlerts(alertQuery)
	if err != nil {
		return nil, err
	}
	
	// Analyze patterns in the arguments/tools that triggered false positives
	// This is a simplified pattern analysis
	patterns := make(map[string]int)
	
	for _, alert := range alerts {
		// Extract patterns from the alert args
		if alert.Tool != "" {
			patterns[fmt.Sprintf("tool:%s", alert.Tool)]++
		}
		
		// Could add more sophisticated pattern analysis here
		// e.g., common argument patterns, time patterns, etc.
	}
	
	// Convert to sorted slice
	var result []string
	for pattern, count := range patterns {
		if count >= 2 { // Only include patterns that appear multiple times
			result = append(result, fmt.Sprintf("%s (%d occurrences)", pattern, count))
		}
	}
	
	sort.Strings(result)
	return result, nil
}

func (re *RefinementEngine) generateSuggestions(stats *RuleStats, patterns []string) []RefinementSuggestion {
	var suggestions []RefinementSuggestion
	
	// Suggest based on false positive rate
	if stats.FalsePositiveRate > 0.5 {
		suggestions = append(suggestions, RefinementSuggestion{
			Type:        "reduce_severity",
			Description: "Rule has very high false positive rate - consider reducing severity",
			Confidence:  0.8,
			Reasoning:   fmt.Sprintf("FP rate: %.1f%% is extremely high", stats.FalsePositiveRate*100),
		})
	} else if stats.FalsePositiveRate > 0.3 {
		suggestions = append(suggestions, RefinementSuggestion{
			Type:        "add_exception",
			Description: "Add exceptions for common false positive patterns",
			Confidence:  0.7,
			Reasoning:   fmt.Sprintf("FP rate: %.1f%% suggests need for refinement", stats.FalsePositiveRate*100),
		})
	}
	
	// Suggest based on patterns
	if len(patterns) > 0 {
		suggestions = append(suggestions, RefinementSuggestion{
			Type:        "narrow_condition",
			Description: "Narrow rule conditions to avoid common false positive patterns",
			Confidence:  0.6,
			Reasoning:   fmt.Sprintf("Found %d recurring patterns: %v", len(patterns), patterns),
		})
	}
	
	return suggestions
}

func (re *RefinementEngine) determineRecommendedAction(fpRate float64) string {
	switch {
	case fpRate > 0.7:
		return "disable"
	case fpRate > 0.4:
		return "refine_urgently"
	case fpRate > 0.2:
		return "refine"
	case fpRate > 0.1:
		return "monitor"
	default:
		return "none"
	}
}

func (re *RefinementEngine) getLLMRefinementSuggestion(ctx context.Context, result *RuleRefinementResult) (*triage.TriageResult, error) {
	// This would need to be implemented if we want LLM-assisted refinement
	// For now, return nil to indicate no LLM analysis
	return nil, fmt.Errorf("LLM refinement analysis not yet implemented")
}

func (re *RefinementEngine) applyRefinementToContent(content string, suggestion *RefinementSuggestion) (string, error) {
	// This would implement the actual rule modification logic
	// For now, return the original content
	return content, fmt.Errorf("automatic rule refinement not yet implemented")
}

func (re *RefinementEngine) validateRuleYAML(content string) error {
	// Basic YAML validation
	var rule interface{}
	return yaml.Unmarshal([]byte(content), &rule)
}

func (re *RefinementEngine) validateRuleFilePath(path string) error {
	// Check for path traversal patterns first
	if strings.Contains(path, "..") {
		return fmt.Errorf("path traversal detected")
	}
	
	// Ensure the path is within the rules directory and doesn't contain traversal
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	
	rulesAbs, err := filepath.Abs(re.rulesDir)
	if err != nil {
		return err
	}
	
	// Clean both paths to resolve any . or .. components that might remain
	abs = filepath.Clean(abs)
	rulesAbs = filepath.Clean(rulesAbs)
	
	if !strings.HasPrefix(abs, rulesAbs) {
		return fmt.Errorf("path outside rules directory")
	}
	
	return nil
}