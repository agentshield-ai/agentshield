package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/store"
)

// staticBody and uniqueBody are the canonical request payload variants:
// staticBody hits the cache; uniqueBody forces a miss every iteration.
const staticBody = `{"event_id":"evt_static","tool":"Bash","args":{"command":"echo hello"},"context":"prod"}`

func uniqueBody(i int) []byte {
	return []byte(fmt.Sprintf(
		`{"event_id":"evt_%d","tool":"Bash","args":{"command":"echo hello %d"},"context":"prod"}`,
		i, i,
	))
}

func benchServer(b *testing.B) (*Server, func()) {
	b.Helper()
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	cfg := &config.Config{EvaluationMode: config.ModeAudit}
	eng := &mockRuleEngine{
		results: []engine.RuleResult{
			{
				RuleID:   "bench-rule-low",
				RuleName: "bench rule low",
				Severity: engine.SeverityLow,
				Matched:  true,
			},
		},
	}
	evaluator := evaluate.NewEvaluator(eng, config.ModeAudit, "", nil, nil)
	evaluator.SetCache(cache.NewVerdictCache(10000, 5*time.Minute))
	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		testStore.Close()
		b.Fatal(err)
	}
	return srv, func() { testStore.Close() }
}

func BenchmarkHandleEvaluate_Direct(b *testing.B) {
	srv, cleanup := benchServer(b)
	defer cleanup()
	router := srv.setupTestRouter()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := uniqueBody(i)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func BenchmarkHandleEvaluate_DirectCacheHit(b *testing.B) {
	srv, cleanup := benchServer(b)
	defer cleanup()
	router := srv.setupTestRouter()

	body := []byte(staticBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), req)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status %d", rec.Code)
		}
	}
}

func BenchmarkHandleEvaluate_HTTP(b *testing.B) {
	srv, cleanup := benchServer(b)
	defer cleanup()
	ts := httptest.NewServer(srv.setupTestRouter())
	defer ts.Close()
	url := ts.URL + "/api/v1/evaluate"
	client := ts.Client()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := uniqueBody(i)
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("unexpected status %d", resp.StatusCode)
		}
	}
}

// BenchmarkHandleEvaluate_Concurrent is the source of the throughput number
// reported in docs/performance.md.
func BenchmarkHandleEvaluate_Concurrent(b *testing.B) {
	srv, cleanup := benchServer(b)
	defer cleanup()
	ts := httptest.NewServer(srv.setupTestRouter())
	defer ts.Close()
	url := ts.URL + "/api/v1/evaluate"
	client := ts.Client()

	var counter atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := counter.Add(1)
			body := uniqueBody(int(i))
			resp, err := client.Post(url, "application/json", bytes.NewReader(body))
			if err != nil {
				b.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b.Fatalf("unexpected status %d", resp.StatusCode)
			}
		}
	})
}
