package replay

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// NewHFFetcher creates a fetcher for the given HF dataset.
func NewHFFetcher(dataset string, pageSize int) *HFFetcher {
	if pageSize <= 0 || pageSize > maxPageSize {
		pageSize = defaultPageSize
	}

	rc := retryablehttp.NewClient()
	rc.RetryMax = 4
	rc.RetryWaitMin = 2 * time.Second
	rc.RetryWaitMax = 16 * time.Second
	rc.Logger = nil // suppress noisy retry logs

	return &HFFetcher{
		dataset:  dataset,
		config:   "default",
		split:    "train",
		pageSize: pageSize,
		client:   rc.StandardClient(),
	}
}

// FetchPage retrieves a page of rows starting at offset.
func (f *HFFetcher) FetchPage(offset int) ([]TraceRow, int, error) {
	url := fmt.Sprintf("%s/rows?dataset=%s&config=%s&split=%s&offset=%d&length=%d",
		hfBaseURL, f.dataset, f.config, f.split, offset, f.pageSize)

	slog.Debug("Fetching HF page", "url", url, "offset", offset)

	resp, err := f.client.Get(url)
	if err != nil {
		return nil, 0, fmt.Errorf("HF API request failed: %w", err)
	}
	defer resp.Body.Close()

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
