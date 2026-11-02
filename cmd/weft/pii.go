package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// piiFlags configure the optional NER analyzer for server-side PII redaction.
type piiFlags struct {
	url, timeout *string
}

func registerPIIFlags() *piiFlags {
	return &piiFlags{
		url: flag.String("pii-analyzer-url", os.Getenv("WEFT_PII_ANALYZER_URL"),
			"Presidio Analyzer REST URL, e.g. http://127.0.0.1:5002 (env WEFT_PII_ANALYZER_URL); adds PERSON, LOCATION and ORGANIZATION redaction to projects whose sensitive-data policy is on"),
		timeout: flag.String("pii-analyzer-timeout", envOr("WEFT_PII_ANALYZER_TIMEOUT", "2s"),
			"time budget for the analyzer per ingest request; on timeout or error calls are stored with regex redaction only (env WEFT_PII_ANALYZER_TIMEOUT)"),
	}
}

// apply sets st's analyzer and returns a description for the startup log.
func (f *piiFlags) apply(st *store.Store) (string, error) {
	if *f.url == "" {
		return "regex", nil
	}
	d, err := time.ParseDuration(*f.timeout)
	if err != nil || d <= 0 {
		return "", fmt.Errorf("invalid --pii-analyzer-timeout %q", *f.timeout)
	}
	st.SetPIIAnalyzer(store.NewPresidioAnalyzer(*f.url), d)
	return "regex+presidio", nil
}
