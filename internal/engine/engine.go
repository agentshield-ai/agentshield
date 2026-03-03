package engine

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	sigma "github.com/agentshield-ai/agentshield/pkg/sigma"
)

// AlertSeverity represents the severity level of an alert
type AlertSeverity string

const (
	SeverityLow      AlertSeverity = "low"
	SeverityMedium   AlertSeverity = "medium"
	SeverityHigh     AlertSeverity = "high"
	SeverityCritical AlertSeverity = "critical"
)

// RuleResult represents the result of evaluating a rule
type RuleResult struct {
	RuleID        string            `json:"rule_id"`
	RuleName      string            `json:"rule_name"`
	Severity      AlertSeverity     `json:"severity"`
	Description   string            `json:"description"`
	Matched       bool              `json:"matched"`
	MatchedFields map[string]string `json:"matched_fields,omitempty"`
}

// LoadedRule represents a loaded Sigma rule with metadata
type LoadedRule struct {
	rule        *sigma.Rule
	id          string
	title       string
	severity    AlertSeverity
	description string
}

// Engine wraps the sigma rule evaluation with security protections
type Engine struct {
	mu        sync.RWMutex
	rules     []LoadedRule
	rulesDir  string
	loadTime  time.Time
	ruleCount int
}

// NewEngine creates a new rule engine
func NewEngine(rulesDir string) (*Engine, error) {
	// Validate and resolve the rules directory path
	absRulesDir, err := filepath.Abs(rulesDir)
	if err != nil {
		return nil, fmt.Errorf("resolving rules directory: %w", err)
	}

	// SECURITY: Ensure rules directory exists and is a directory
	stat, err := os.Stat(absRulesDir)
	if err != nil {
		return nil, fmt.Errorf("accessing rules directory %s: %w", absRulesDir, err)
	}
	if !stat.IsDir() {
		return nil, fmt.Errorf("rules path is not a directory: %s", absRulesDir)
	}

	engine := &Engine{
		rulesDir: absRulesDir,
	}

	// Load rules initially
	if err := engine.LoadRules(); err != nil {
		return nil, fmt.Errorf("initial rule loading: %w", err)
	}

	return engine, nil
}

// LoadRules loads all Sigma rules from the rules directory
func (e *Engine) LoadRules() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	slog.Info("Loading rules", "directory", e.rulesDir)

	var newRules []LoadedRule
	loadStartTime := time.Now()

	err := filepath.WalkDir(e.rulesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			slog.Warn("Error accessing path", "path", path, "error", err)
			return nil // Continue walking
		}

		// Skip directories
		if d.IsDir() {
			return nil
		}

		// Only process YAML files
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}

		// SECURITY: Path traversal protection - ensure file is within rules directory
		if !e.isPathSafe(path) {
			slog.Warn("Skipping file outside rules directory", "path", path)
			return nil
		}

		// Load and validate the rule
		rule, err := e.loadRuleFile(path)
		if err != nil {
			slog.Warn("Failed to load rule", "path", path, "error", err)
			return nil // Continue with other rules
		}

		newRules = append(newRules, *rule)
		return nil
	})

	if err != nil {
		return fmt.Errorf("walking rules directory: %w", err)
	}

	// Update engine state
	e.rules = newRules
	e.loadTime = loadStartTime
	e.ruleCount = len(newRules)

	slog.Info("Successfully loaded rules", "count", len(newRules), "duration", time.Since(loadStartTime))
	return nil
}

// isPathSafe checks if a file path is within the rules directory (prevents path traversal)
func (e *Engine) isPathSafe(filePath string) bool {
	// Get absolute path
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		slog.Warn("Failed to resolve absolute path", "path", filePath, "error", err)
		return false
	}

	// Check if the path is within the rules directory
	relPath, err := filepath.Rel(e.rulesDir, absPath)
	if err != nil {
		slog.Warn("Failed to compute relative path", "path", absPath, "error", err)
		return false
	}

	// Path is safe if it doesn't start with ".." (i.e., doesn't go up from rules dir)
	return !strings.HasPrefix(relPath, "..") && relPath != ".."
}

// loadRuleFile loads a single Sigma rule file with validation
func (e *Engine) loadRuleFile(filePath string) (*LoadedRule, error) {
	// Read file content
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	// Parse Sigma rule
	rule, err := sigma.ParseRule(content)
	if err != nil {
		return nil, fmt.Errorf("parsing Sigma rule: %w", err)
	}

	// Validate rule has detection logic
	if rule.Detection == nil {
		return nil, fmt.Errorf("rule missing detection block")
	}

	// SECURITY: ReDoS protection - validate regex patterns for complexity
	if err := e.validateRuleRegexComplexity(rule); err != nil {
		return nil, fmt.Errorf("rule contains unsafe regex patterns: %w", err)
	}

	// Extract metadata
	ruleID := rule.ID
	if ruleID == "" {
		// Use filename as ID if not specified
		ruleID = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}

	severity := e.mapSeverity(rule.Level)
	description := rule.Description
	if description == "" {
		description = rule.Title
	}

	return &LoadedRule{
		rule:        rule,
		id:          ruleID,
		title:       rule.Title,
		severity:    severity,
		description: description,
	}, nil
}

// extractSearchAtoms walks the expression tree and collects all SearchAtom nodes.
func extractSearchAtoms(expr sigma.Expr) []*sigma.SearchAtom {
	var atoms []*sigma.SearchAtom
	switch e := expr.(type) {
	case *sigma.SearchAtom:
		atoms = append(atoms, e)
	case *sigma.AndExpr:
		for _, x := range e.X {
			atoms = append(atoms, extractSearchAtoms(x)...)
		}
	case *sigma.OrExpr:
		for _, x := range e.X {
			atoms = append(atoms, extractSearchAtoms(x)...)
		}
	case *sigma.NotExpr:
		atoms = append(atoms, extractSearchAtoms(e.X)...)
	case *sigma.NamedExpr:
		atoms = append(atoms, extractSearchAtoms(e.X)...)
	}
	return atoms
}

// validateRuleRegexComplexity validates regex patterns in |re modified atoms
// to prevent ReDoS attacks. It walks the parsed detection expression tree
// rather than relying on string-formatting the rule.
func (e *Engine) validateRuleRegexComplexity(rule *sigma.Rule) error {
	if rule.Detection == nil || rule.Detection.Expr == nil {
		return nil
	}

	atoms := extractSearchAtoms(rule.Detection.Expr)

	// Known dangerous nested-quantifier patterns.
	dangerousPatterns := []string{
		`(.*)*`,
		`(.+)+`,
		`(.{0,})*`,
		`(a|a)*`,
		`(a*)*`,
		`(a+)+`,
		`(a{0,}){0,}`,
	}

	for _, atom := range atoms {
		hasRe := false
		for _, mod := range atom.Modifiers {
			if mod == "re" {
				hasRe = true
				break
			}
		}
		if !hasRe {
			continue
		}

		for _, pattern := range atom.Patterns {
			// Check pattern length.
			if len(pattern) > 1000 {
				return fmt.Errorf("regex pattern too long (%d chars, max 1000)", len(pattern))
			}

			// Check for dangerous nested-quantifier patterns.
			lower := strings.ToLower(pattern)
			for _, dangerous := range dangerousPatterns {
				if strings.Contains(lower, dangerous) {
					return fmt.Errorf("potentially dangerous regex pattern detected: %s", dangerous)
				}
			}

			// Verify the pattern compiles (Go's RE2 rejects some pathological patterns).
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("invalid regex pattern: %w", err)
			}
		}
	}

	return nil
}

// mapSeverity maps Sigma severity levels to our AlertSeverity enum
func (e *Engine) mapSeverity(level sigma.Level) AlertSeverity {
	// Fast path: standard Sigma Level constants are already lowercase.
	switch level {
	case sigma.Informational:
		return SeverityLow
	case sigma.Low:
		return SeverityLow
	case sigma.Medium:
		return SeverityMedium
	case sigma.High:
		return SeverityHigh
	case sigma.Critical:
		return SeverityCritical
	}
	// Slow path: handle non-standard casing or abbreviations.
	switch strings.ToLower(string(level)) {
	case "informational", "info", "low":
		return SeverityLow
	case "medium", "med":
		return SeverityMedium
	case "high":
		return SeverityHigh
	case "critical", "crit":
		return SeverityCritical
	default:
		return SeverityMedium
	}
}

// Evaluate evaluates an event against all loaded rules.
// Returns only rules that matched the input fields.
func (e *Engine) Evaluate(fields map[string]string) []RuleResult {
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()

	// Create log entry once for all rules.
	entry := &sigma.LogEntry{
		Fields: fields,
	}

	var results []RuleResult
	for i := range rules {
		if rules[i].rule.Detection.Matches(entry, nil) {
			// Shallow-copy fields to avoid leaking the caller's map reference
			// (which may contain sensitive args not relevant to this rule match).
			matched := make(map[string]string, len(fields))
			for k, v := range fields {
				matched[k] = v
			}
			results = append(results, RuleResult{
				RuleID:        rules[i].id,
				RuleName:      rules[i].title,
				Severity:      rules[i].severity,
				Description:   rules[i].description,
				Matched:       true,
				MatchedFields: matched,
			})
		}
	}

	return results
}

// GetLoadedRules returns information about currently loaded rules
func (e *Engine) GetLoadedRules() []RuleResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rules := make([]RuleResult, 0, len(e.rules))
	for _, rule := range e.rules {
		rules = append(rules, RuleResult{
			RuleID:      rule.id,
			RuleName:    rule.title,
			Severity:    rule.severity,
			Description: rule.description,
			Matched:     false, // Not evaluated yet
		})
	}

	return rules
}

// GetStats returns engine statistics
func (e *Engine) GetStats() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return map[string]interface{}{
		"rules_loaded": e.ruleCount,
		"rules_dir":    e.rulesDir,
		"last_load":    e.loadTime.Format(time.RFC3339),
		"uptime":       time.Since(e.loadTime).Seconds(),
	}
}
