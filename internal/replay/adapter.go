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

// LabelledAdapter is implemented by adapters over corpora that carry ground
// truth, which unlocks the scoring section of the report. Adapters that do not
// implement it yield an unlabelled run and no scoring.
type LabelledAdapter interface {
	TraceAdapter
	// ExtractLabelled returns the trace's ground-truth label alongside its events.
	ExtractLabelled(row map[string]interface{}) (TraceLabel, []ExtractedEvent, error)
}

// adapterRegistry maps known dataset name prefixes to their adapters.
var adapterRegistry = map[string]func() TraceAdapter{
	"nlile/":        func() TraceAdapter { return &NlileAdapter{} },
	"sammshen/":     func() TraceAdapter { return &WildClawAdapter{} },
	"smolagents/":   func() TraceAdapter { return &SmolAgentsAdapter{} },
	"AI45Research/": func() TraceAdapter { return &ATBenchAdapter{} },
}

// datasetView is the HF Dataset Viewer config and split for a dataset. The
// viewer defaults of "default"/"train" are wrong for several corpora, so each
// dataset names its own view.
type datasetView struct {
	Config string
	Split  string
}

// defaultDatasetView is the HF Dataset Viewer's own default.
var defaultDatasetView = datasetView{Config: "default", Split: "train"}

// datasetViews maps a dataset prefix to the config and split that actually
// exist for it. ATBench publishes no "default" config and no "train" split.
var datasetViews = map[string]datasetView{
	"AI45Research/ATBench": {Config: "ATBench", Split: "test"},
}

// ViewForDataset returns the HF config and split to request for a dataset.
func ViewForDataset(dataset string) datasetView {
	if v, ok := datasetViews[dataset]; ok {
		return v
	}
	for prefix, v := range datasetViews {
		if strings.HasPrefix(dataset, prefix) {
			return v
		}
	}
	return defaultDatasetView
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
	case strings.Contains(lower, "atbench"):
		return &ATBenchAdapter{}, nil
	case strings.Contains(lower, "wildclaw") || strings.Contains(lower, "opus-traces"):
		return &WildClawAdapter{}, nil
	case strings.Contains(lower, "smolagent"):
		return &SmolAgentsAdapter{}, nil
	case strings.Contains(lower, "claude") || strings.Contains(lower, "trace"):
		return &NlileAdapter{}, nil
	}
	return nil, fmt.Errorf("no adapter found for dataset %q; supported prefixes: nlile/, sammshen/, smolagents/, AI45Research/", dataset)
}

// appendCallWithResult appends a tool-call event and, when the call carries the
// tool's output, a second event representing that output.
//
// Production delivers a call and its result down two different paths: the call
// to /api/v1/evaluate before execution, and the result to /api/v1/audit after,
// where internal/toolresult.DetectionFields turns it into an event_type of
// tool_response carrying response and content. A corpus that only ever yields
// call events therefore cannot exercise any rule keyed on tool output, and
// cannot measure what such a rule would cost on benign traffic.
//
// The output text is deliberately left on the call event as well. It is marked
// replay-only there, because a pre-execution evaluation cannot have seen the
// tool's output yet, and dropping it would silently change what the existing
// corpora detect. See fieldmap.go for how the two are told apart.
func appendCallWithResult(events []ExtractedEvent, call ExtractedEvent) []ExtractedEvent {
	if call.Kind == "" {
		call.Kind = EventKindCall
	}
	events = append(events, call)
	if call.Content == "" {
		return events
	}
	return append(events, ExtractedEvent{
		SessionID: call.SessionID,
		Kind:      EventKindResult,
		ToolName:  call.ToolName,
		Args:      map[string]string{},
		Response:  call.Content,
		Content:   call.Content,
	})
}
