package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/daemon"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/feedback"
	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/agentshield-ai/agentshield/internal/triage"
)

var (
	version   = "1.0.0"
	buildTime = "unknown"
	gitHash   = "unknown"

	// Global flags
	configFile string
	verbose    bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "agentshield",
		Short:   "AgentShield security engine",
		Long:    "AgentShield is a real-time security evaluation engine for AI agents",
		Version: version,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "config file path")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "verbose output")

	// Add subcommands
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newAlertsCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newRulesCmd())
	rootCmd.AddCommand(newRefineCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// newServeCmd creates the serve command
func newServeCmd() *cobra.Command {
	var (
		daemonMode bool
		pidFile    string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the AgentShield server",
		Long:  "Start the AgentShield server to evaluate security events",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load configuration
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Setup logging
			setupLogging(cfg.LogLevel)

			// Create daemon
			d, err := daemon.NewDaemon(cfg)
			if err != nil {
				return fmt.Errorf("creating daemon: %w", err)
			}

			// Start daemon
			return d.Start()
		},
	}

	cmd.Flags().BoolVarP(&daemonMode, "daemon", "d", true, "run in daemon mode")
	cmd.Flags().StringVar(&pidFile, "pid-file", "", "PID file path (default: auto)")

	return cmd
}

// newStatusCmd creates the status command
func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check server status",
		Long:  "Check if the AgentShield server is running and show health info",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Check daemon status
			d, err := daemon.NewDaemon(cfg)
			if err != nil {
				return fmt.Errorf("creating daemon: %w", err)
			}

			if err := d.Status(); err != nil {
				fmt.Printf("Daemon: %v\n", err)
			}

			// Check HTTP health
			healthURL := fmt.Sprintf("http://%s/api/v1/health", cfg.ListenAddr())
			
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(healthURL)
			if err != nil {
				fmt.Printf("HTTP Health: ERROR - %v\n", err)
				return nil
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				var health map[string]interface{}
				if err := json.NewDecoder(resp.Body).Decode(&health); err == nil {
					fmt.Printf("HTTP Health: OK\n")
					if verbose {
						healthJSON, _ := json.MarshalIndent(health, "", "  ")
						fmt.Printf("Health Details:\n%s\n", healthJSON)
					}
				}
			} else {
				fmt.Printf("HTTP Health: ERROR - status %d\n", resp.StatusCode)
			}

			return nil
		},
	}

	return cmd
}

// newAlertsCmd creates the alerts command
func newAlertsCmd() *cobra.Command {
	var (
		limit    int
		severity string
		since    string
		rule     string
	)

	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "List recent alerts",
		Long:  "List recent security alerts from the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Connect to store
			st, err := store.NewStore(cfg.Store.SQLitePath)
			if err != nil {
				return fmt.Errorf("connecting to store: %w", err)
			}
			defer st.Close()

			// Build query
			query := &store.AlertQuery{
				Limit:    limit,
				Severity: severity,
				Rule:     rule,
			}

			// Parse since timestamp
			if since != "" {
				if sinceTime, err := time.Parse(time.RFC3339, since); err == nil {
					query.Since = &sinceTime
				} else if sinceTime, err := time.Parse("2006-01-02", since); err == nil {
					query.Since = &sinceTime
				} else {
					return fmt.Errorf("invalid since format (use RFC3339 or YYYY-MM-DD): %s", since)
				}
			}

			// Query alerts
			alerts, err := st.QueryAlerts(query)
			if err != nil {
				return fmt.Errorf("querying alerts: %w", err)
			}

			// Display results
			if len(alerts) == 0 {
				fmt.Printf("No alerts found\n")
				return nil
			}

			fmt.Printf("Found %d alerts:\n\n", len(alerts))
			for _, alert := range alerts {
				fmt.Printf("ID: %d\n", alert.ID)
				fmt.Printf("Rule: %s\n", alert.RuleName)
				fmt.Printf("Severity: %s\n", alert.Severity)
				fmt.Printf("Action: %s\n", alert.ActionTaken)
				fmt.Printf("Time: %s\n", alert.Timestamp.Format(time.RFC3339))
				if alert.Tool != "" {
					fmt.Printf("Tool: %s\n", alert.Tool)
				}
				if alert.SessionID != "" {
					fmt.Printf("Session: %s\n", alert.SessionID)
				}
				fmt.Printf("Event: %s\n", alert.EventID)
				fmt.Printf("\n")
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "l", 20, "maximum number of alerts to show")
	cmd.Flags().StringVarP(&severity, "severity", "s", "", "filter by severity (low,medium,high,critical)")
	cmd.Flags().StringVar(&since, "since", "", "show alerts since timestamp (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVarP(&rule, "rule", "r", "", "filter by rule name")

	return cmd
}

// newVersionCmd creates the version command
func newVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long:  "Show version, build time, and git hash information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("AgentShield Engine\n")
			fmt.Printf("Version: %s\n", version)
			fmt.Printf("Build Time: %s\n", buildTime)
			fmt.Printf("Git Hash: %s\n", gitHash)
		},
	}

	return cmd
}

// newRulesCmd creates the rules command group
func newRulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Manage security rules",
		Long:  "Commands for managing and inspecting security rules",
	}

	cmd.AddCommand(newRulesListCmd())
	cmd.AddCommand(newRulesReloadCmd())

	return cmd
}

// newRulesListCmd creates the rules list command
func newRulesListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List loaded rules",
		Long:  "List all currently loaded security rules",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Load engine to get rules
			eng, err := engine.NewEngine(cfg.Rules.Dir)
			if err != nil {
				return fmt.Errorf("loading engine: %w", err)
			}

			rules := eng.GetLoadedRules()
			if len(rules) == 0 {
				fmt.Printf("No rules loaded from %s\n", cfg.Rules.Dir)
				return nil
			}

			fmt.Printf("Loaded %d rules from %s:\n\n", len(rules), cfg.Rules.Dir)
			for _, rule := range rules {
				fmt.Printf("ID: %s\n", rule.RuleID)
				fmt.Printf("Name: %s\n", rule.RuleName)
				fmt.Printf("Severity: %s\n", rule.Severity)
				if rule.Description != "" && rule.Description != rule.RuleName {
					fmt.Printf("Description: %s\n", rule.Description)
				}
				fmt.Printf("\n")
			}

			return nil
		},
	}

	return cmd
}

// newRulesReloadCmd creates the rules reload command
func newRulesReloadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reload",
		Short: "Reload rules",
		Long:  "Send SIGHUP to the running server to reload rules",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			d, err := daemon.NewDaemon(cfg)
			if err != nil {
				return fmt.Errorf("creating daemon: %w", err)
			}

			return d.Reload()
		},
	}

	return cmd
}

// loadConfig loads the configuration
func loadConfig() (*config.Config, error) {
	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return nil, err
	}

	if verbose {
		fmt.Printf("Loaded config: addr=%s, mode=%s, rules=%s, db=%s, auth=%t, triage=%t\n",
			cfg.ListenAddr(),
			cfg.EvaluationMode,
			cfg.Rules.Dir,
			cfg.Store.SQLitePath,
			cfg.Auth.Token != "",
			cfg.Triage.Enabled,
		)
	}

	return cfg, nil
}

// setupLogging configures the log output
func setupLogging(level string) {
	// Basic logging setup - in production you might want structured logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	
	// Set log level (basic implementation)
	switch strings.ToLower(level) {
	case "debug":
		// Enable all logging
	case "info":
		// Default level
	case "warn", "warning":
		// Could filter out debug/info logs
	case "error":
		// Could filter out debug/info/warn logs  
	}

	if verbose {
		log.Printf("Log level set to: %s", level)
	}
}

// newRefineCmd creates the refine command
func newRefineCmd() *cobra.Command {
	var (
		apply     bool
		threshold float64
	)

	cmd := &cobra.Command{
		Use:   "refine [rule-name]",
		Short: "Analyze and refine security rules",
		Long:  "Analyze rule performance and suggest improvements to reduce false positives",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			// Connect to store
			st, err := store.NewStore(cfg.Store.SQLitePath)
			if err != nil {
				return fmt.Errorf("connecting to store: %w", err)
			}
			defer st.Close()

			// Create feedback manager
			feedbackManager := feedback.NewFeedbackManager(st)

			// Create triager (if enabled)
			var triager *triage.Triager
			if cfg.Triage.Enabled {
				triager, err = triage.NewTriager(&cfg.Triage, st)
				if err != nil {
					log.Printf("WARN: Failed to create triager: %v", err)
				}
			}

			// Create refinement engine
			refinementEngine := feedback.NewRefinementEngine(feedbackManager, cfg.Rules.Dir, triager)

			ctx := context.Background()

			if len(args) == 1 {
				// Analyze specific rule
				ruleName := args[0]
				result, err := refinementEngine.AnalyzeRule(ctx, ruleName)
				if err != nil {
					return fmt.Errorf("analyzing rule %s: %w", ruleName, err)
				}

				// Display results
				fmt.Printf("Rule Analysis: %s\n", result.RuleName)
				fmt.Printf("═══════════════════════════════════════\n")
				fmt.Printf("File: %s\n", result.RuleFile)
				fmt.Printf("False Positive Rate: %.1f%%\n", result.FalsePositiveRate*100)
				fmt.Printf("Total Alerts: %d\n", result.TotalAlerts)
				fmt.Printf("Feedback Count: %d\n", result.FeedbackCount)
				fmt.Printf("Recommended Action: %s\n", result.RecommendedAction)

				if len(result.CommonPatterns) > 0 {
					fmt.Printf("\nCommon False Positive Patterns:\n")
					for _, pattern := range result.CommonPatterns {
						fmt.Printf("  • %s\n", pattern)
					}
				}

				if len(result.Suggestions) > 0 {
					fmt.Printf("\nSuggestions:\n")
					for i, suggestion := range result.Suggestions {
						fmt.Printf("  %d. %s\n", i+1, suggestion.Description)
						fmt.Printf("     Type: %s (confidence: %.1f%%)\n", suggestion.Type, suggestion.Confidence*100)
						fmt.Printf("     Reasoning: %s\n", suggestion.Reasoning)
					}
				}

				if result.LLMAnalysis != nil {
					fmt.Printf("\nLLM Analysis:\n")
					fmt.Printf("  Verdict: %s (confidence: %.1f%%)\n", result.LLMAnalysis.Verdict, result.LLMAnalysis.Confidence*100)
					fmt.Printf("  Reasoning: %s\n", result.LLMAnalysis.Reasoning)
					fmt.Printf("  Suggested Action: %s\n", result.LLMAnalysis.SuggestedAction)
				}

				// Apply refinement if requested
				if apply && len(result.Suggestions) > 0 {
					fmt.Printf("\nApplying first suggestion...\n")
					err := refinementEngine.ApplyRefinement(ruleName, &result.Suggestions[0], true)
					if err != nil {
						return fmt.Errorf("applying refinement: %w", err)
					}
					fmt.Printf("✅ Refinement applied successfully (backup created)\n")
				}

			} else {
				// List rules needing refinement
				results, err := refinementEngine.GetRulesNeedingRefinement(threshold)
				if err != nil {
					return fmt.Errorf("getting rules needing refinement: %w", err)
				}

				if len(results) == 0 {
					fmt.Printf("No rules found with FP rate >= %.1f%%\n", threshold*100)
					return nil
				}

				fmt.Printf("Rules needing refinement (FP rate >= %.1f%%):\n", threshold*100)
				fmt.Printf("═══════════════════════════════════════════════\n")
				for _, result := range results {
					fmt.Printf("%-30s FP: %5.1f%% Alerts: %4d Action: %s\n",
						result.RuleName,
						result.FalsePositiveRate*100,
						result.TotalAlerts,
						result.RecommendedAction)
				}
				fmt.Printf("\nUse 'agentshield refine <rule-name>' for detailed analysis\n")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "apply the first suggested refinement")
	cmd.Flags().Float64Var(&threshold, "threshold", 0.3, "false positive rate threshold for listing rules")

	return cmd
}