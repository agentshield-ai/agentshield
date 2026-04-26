// agentshield-proxy-adapter ingests forward-proxy observations (CrabTrap,
// mitmproxy, etc.) from stdin as NDJSON and forwards each as an evaluation
// request to a running AgentShield engine. The result is a single audit
// trail and verdict cache spanning both tool-call events (from agent
// hooks) and wire-level events (from the proxy).
//
// Typical deployment: pipe the proxy's structured log through a small jq
// transform that produces the schema this adapter consumes, then into the
// adapter:
//
//	tail -F /var/log/crabtrap.json \
//	  | jq -c '{request_id:.req_id, method:.method, url:.url, ...}' \
//	  | agentshield-proxy-adapter \
//	      --endpoint http://localhost:8433 \
//	      --token "$AGENTSHIELD_AUTH_TOKEN" \
//	      --source crabtrap
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agentshield-ai/agentshield/internal/proxyadapter"
)

var version = "0.1.0"

func main() {
	var (
		endpoint string
		token    string
		source   string
	)

	cmd := &cobra.Command{
		Use:     "agentshield-proxy-adapter",
		Short:   "Ingest forward-proxy observations into AgentShield",
		Long:    "Reads NDJSON proxy observations from stdin and forwards each as an evaluation request to the AgentShield engine.",
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			if endpoint == "" {
				return fmt.Errorf("--endpoint is required")
			}
			if source == "" {
				return fmt.Errorf("--source is required (e.g. crabtrap, mitmproxy)")
			}
			if token == "" {
				token = os.Getenv("AGENTSHIELD_AUTH_TOKEN")
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			client := proxyadapter.NewClient(endpoint, token)
			stats, err := proxyadapter.Run(ctx, os.Stdin, client, source)
			fmt.Fprintf(os.Stderr,
				"proxyadapter: lines=%d parsed=%d forwarded=%d dropped=%d\n",
				stats.Lines, stats.Parsed, stats.Forwarded, stats.Dropped)
			return err
		},
	}

	cmd.Flags().StringVar(&endpoint, "endpoint", "http://localhost:8433", "AgentShield engine endpoint")
	cmd.Flags().StringVar(&token, "token", "", "Bearer auth token (or AGENTSHIELD_AUTH_TOKEN env)")
	cmd.Flags().StringVar(&source, "source", "", "Source label for proxy.* fields (e.g. crabtrap, mitmproxy)")

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
