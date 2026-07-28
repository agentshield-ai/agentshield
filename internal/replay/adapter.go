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
