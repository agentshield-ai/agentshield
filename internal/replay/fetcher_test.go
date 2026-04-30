package replay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHFFetcher_FetchPage(t *testing.T) {
	// Mock HF API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("dataset") != "test/dataset" {
			t.Errorf("unexpected dataset: %s", q.Get("dataset"))
		}

		resp := HFRowsResponse{
			Features: []HFFeature{{Name: "col1", Type: "Value"}},
			Rows: []TraceRow{
				{RowIdx: 0, Row: map[string]interface{}{"col1": "val1"}},
				{RowIdx: 1, Row: map[string]interface{}{"col1": "val2"}},
			},
			NumRows: 100,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create fetcher pointing at mock server
	f := &HFFetcher{
		dataset:  "test/dataset",
		config:   "default",
		split:    "train",
		pageSize: 100,
		client:   server.Client(),
	}

	// Override base URL by using a custom transport that redirects to test server
	origClient := f.client
	f.client = &http.Client{
		Transport: &rewriteTransport{base: server.URL, inner: origClient.Transport},
	}

	rows, total, err := f.FetchPage(0)
	if err != nil {
		t.Fatalf("FetchPage error: %v", err)
	}
	if total != 100 {
		t.Errorf("total = %d, want 100", total)
	}
	if len(rows) != 2 {
		t.Errorf("got %d rows, want 2", len(rows))
	}
	if rows[0].Row["col1"] != "val1" {
		t.Errorf("row[0].col1 = %v, want val1", rows[0].Row["col1"])
	}
}

func TestHFFetcher_FetchPage_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("dataset not found"))
	}))
	defer server.Close()

	f := &HFFetcher{
		dataset:  "bad/dataset",
		config:   "default",
		split:    "train",
		pageSize: 100,
		client: &http.Client{
			Transport: &rewriteTransport{base: server.URL, inner: http.DefaultTransport},
		},
	}

	_, _, err := f.FetchPage(0)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// rewriteTransport redirects all requests to the test server.
type rewriteTransport struct {
	base  string
	inner http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = t.base[len("http://"):]
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(req)
}
