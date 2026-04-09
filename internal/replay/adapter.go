package replay

import (
	"fmt"
	"strings"
)

// TraceAdapter extracts EvaluationRequest-ready events from a raw HF dataset row.
type TraceAdapter interface {
	// Name returns the adapter's identifier.
	Name() string
	// Extract parses a single dataset row and returns zero or more events.
	Extract(row map[string]interface{}) ([]ExtractedEvent, error)
}

// adapterRegistry maps known dataset name prefixes to their adapters.
var adapterRegistry = map[string]func() TraceAdapter{
	"nlile/":       func() TraceAdapter { return &NlileAdapter{} },
	"sammshen/":    func() TraceAdapter { return &WildClawAdapter{} },
	"smolagents/":  func() TraceAdapter { return &SmolAgentsAdapter{} },
}

// SelectAdapter returns the appropriate adapter for a dataset name.
// Falls back to NlileAdapter (most common format) if no match is found.
func SelectAdapter(dataset string) (TraceAdapter, error) {
	for prefix, factory := range adapterRegistry {
		if strings.HasPrefix(dataset, prefix) {
			return factory(), nil
		}
	}
	// Try to infer from dataset name patterns
	lower := strings.ToLower(dataset)
	switch {
	case strings.Contains(lower, "wildclaw") || strings.Contains(lower, "opus-traces"):
		return &WildClawAdapter{}, nil
	case strings.Contains(lower, "smolagent"):
		return &SmolAgentsAdapter{}, nil
	case strings.Contains(lower, "claude") || strings.Contains(lower, "trace"):
		return &NlileAdapter{}, nil
	}
	return nil, fmt.Errorf("no adapter found for dataset %q; supported prefixes: nlile/, sammshen/, smolagents/", dataset)
}
