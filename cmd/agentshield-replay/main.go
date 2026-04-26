package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/agentshield-ai/agentshield/internal/replay"
)

var version = "0.1.0"

func main() {
	rootCmd := &cobra.Command{
		Use:     "agentshield-replay",
		Short:   "Replay HuggingFace agent traces through AgentShield rules",
		Version: version,
	}

	rootCmd.AddCommand(newRunCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newRunCmd() *cobra.Command {
	var cfg replay.RunConfig

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Fetch traces, replay through engine, and generate report",
		Long: `Downloads public agent trace datasets from HuggingFace, extracts tool calls,
evaluates them against AgentShield Sigma rules, and produces a coverage/quality report.

Supported datasets:
  - nlile/misc-merged-claude-code-traces-v1  (32K Claude Code conversations)
  - sammshen/wildclaw-opus-traces            (OpenClaw + Claude Opus HTTP traces)
  - smolagents/synthetic-traces-toolcalling  (Synthetic smolagents traces)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg.Dataset == "" {
				return fmt.Errorf("--dataset is required")
			}
			if cfg.RulesDir == "" {
				cfg.RulesDir = "rules/rules"
			}
			if cfg.HTTPMode && cfg.Endpoint == "" {
				cfg.Endpoint = "http://localhost:8433"
			}

			report, err := replay.Run(cfg)
			if err != nil {
				return err
			}

			// Output report
			var output []byte
			switch cfg.OutputFormat {
			case "yaml":
				output, err = yaml.Marshal(report)
			default:
				output, err = json.MarshalIndent(report, "", "  ")
			}
			if err != nil {
				return fmt.Errorf("marshaling report: %w", err)
			}

			if cfg.OutputPath != "" {
				if err := os.WriteFile(cfg.OutputPath, output, 0o644); err != nil {
					return fmt.Errorf("writing report: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Report written to %s\n", cfg.OutputPath)
			} else {
				fmt.Println(string(output))
			}

			return nil
		},
	}

	// Required
	cmd.Flags().StringVar(&cfg.Dataset, "dataset", "", "HuggingFace dataset name (e.g. nlile/misc-merged-claude-code-traces-v1)")
	cmd.MarkFlagRequired("dataset")

	// Engine
	cmd.Flags().StringVar(&cfg.RulesDir, "rules-dir", "rules/rules", "Path to Sigma rules directory")
	cmd.Flags().StringVar(&cfg.Mode, "mode", "audit", "Evaluation mode: audit, enforce, shadow")
	cmd.Flags().BoolVar(&cfg.EnableSessions, "sessions", false, "Enable session behavioral tracking")

	// Data
	cmd.Flags().IntVar(&cfg.MaxTraces, "max-traces", 0, "Maximum traces to process (0 = all)")
	cmd.Flags().IntVar(&cfg.PageSize, "page-size", 100, "HF API page size (max 100)")

	// Output
	cmd.Flags().StringVar(&cfg.OutputFormat, "format", "json", "Output format: json, yaml")
	cmd.Flags().StringVar(&cfg.OutputPath, "output", "", "Output file path (default: stdout)")
	cmd.Flags().StringVar(&cfg.ExportTestcases, "export-testcases", "", "Export triggered traces as bench YAML testcases to this directory")

	// HTTP mode
	cmd.Flags().BoolVar(&cfg.HTTPMode, "http", false, "Use HTTP API mode instead of library mode")
	cmd.Flags().StringVar(&cfg.Endpoint, "endpoint", "http://localhost:8433", "Engine HTTP endpoint (for --http mode)")
	cmd.Flags().StringVar(&cfg.AuthToken, "token", "", "Auth token (for --http mode, or AGENTSHIELD_AUTH_TOKEN env)")

	// Verdict cache (library mode)
	cmd.Flags().IntVar(&cfg.CacheSize, "cache-size", 0, "Verdict cache capacity (0 = default 10000)")
	cmd.Flags().DurationVar(&cfg.CacheTTL, "cache-ttl", 0, "Verdict cache TTL (0 = default 5m)")
	cmd.Flags().BoolVar(&cfg.DisableCache, "no-cache", false, "Disable verdict cache (control runs)")

	// Debug
	cmd.Flags().BoolVar(&cfg.Verbose, "verbose", false, "Verbose output")

	return cmd
}
