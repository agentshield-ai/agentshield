package replay

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"time"

	retryablehttp "github.com/hashicorp/go-retryablehttp"
)

const (
	hfBaseURL       = "https://datasets-server.huggingface.co"
	defaultPageSize = 100
	maxPageSize     = 100
	pageDelay       = 500 * time.Millisecond
)

// Fetcher retrieves trace rows from HuggingFace datasets.
type Fetcher interface {
	// FetchPage returns rows starting at offset, plus the total row count.
	FetchPage(offset int) ([]TraceRow, int, error)
}

// HFFetcher fetches from the HuggingFace Dataset Viewer /rows API.
type HFFetcher struct {
	dataset  string
	config   string // HF dataset config (default: "default")
	split    string // HF dataset split (default: "train")
	pageSize int
	client   *http.Client
}

// NewHFFetcher creates a fetcher for the given HF dataset, using the dataset's
// own Dataset Viewer config and split. The viewer's "default"/"train" defaults
// do not exist for every corpus.
func NewHFFetcher(dataset string, pageSize int) *HFFetcher {
	view := ViewForDataset(dataset)
	return NewHFFetcherWithView(dataset, pageSize, view.Config, view.Split)
}

// NewHFFetcherWithView creates a fetcher with an explicit config and split.
// Empty config or split falls back to the dataset's registered view.
func NewHFFetcherWithView(dataset string, pageSize int, config, split string) *HFFetcher {
	if pageSize <= 0 || pageSize > maxPageSize {
		pageSize = defaultPageSize
	}
	view := ViewForDataset(dataset)
	if config == "" {
		config = view.Config
	}
	if split == "" {
		split = view.Split
	}

	rc := retryablehttp.NewClient()
	rc.RetryMax = 4
	rc.RetryWaitMin = 2 * time.Second
	rc.RetryWaitMax = 16 * time.Second
	rc.Logger = nil // suppress noisy retry logs

	return &HFFetcher{
		dataset:  dataset,
		config:   config,
		split:    split,
		pageSize: pageSize,
		client:   rc.StandardClient(),
	}
}

// View returns the config and split this fetcher will request.
func (f *HFFetcher) View() (config, split string) { return f.config, f.split }

// FetchPage retrieves a page of rows starting at offset.
func (f *HFFetcher) FetchPage(offset int) ([]TraceRow, int, error) {
	url := fmt.Sprintf("%s/rows?dataset=%s&config=%s&split=%s&offset=%d&length=%d",
		hfBaseURL, neturl.QueryEscape(f.dataset), neturl.QueryEscape(f.config),
		neturl.QueryEscape(f.split), offset, f.pageSize)

	slog.Debug("Fetching HF page", "url", url, "offset", offset)

	resp, err := f.client.Get(url)
	if err != nil {
		return nil, 0, fmt.Errorf("HF API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, 0, fmt.Errorf("HF API returned %d: %s", resp.StatusCode, string(body))
	}

	var hfResp HFRowsResponse
	if err := json.NewDecoder(resp.Body).Decode(&hfResp); err != nil {
		return nil, 0, fmt.Errorf("decoding HF response: %w", err)
	}

	return hfResp.Rows, hfResp.NumRows, nil
}

// PageDelay returns the recommended delay between page fetches.
func PageDelay() time.Duration {
	return pageDelay
}
