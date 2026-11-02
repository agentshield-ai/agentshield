package evals

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// RemoteScorer URLs must use https unless the operator allows http, as in
// Weave (WF_SCORING_WORKER_REMOTE_SCORER_ALLOW_INSECURE_HTTP=false by default).
func TestRemoteScorerRequiresHTTPS(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"score": 1}`))
	}))
	defer srv.Close()

	prev := AllowInsecureRemoteScorerHTTP
	defer func() { AllowInsecureRemoteScorerHTTP = prev }()

	val := map[string]any{"url": srv.URL + "/score"}
	AllowInsecureRemoteScorerHTTP = false
	_, err := scoreRemote(nil, context.Background(), "e/p", val, map[string]any{"q": 1}, "out", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("http url allowed by default: err=%v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("server was called %d times before the scheme check", hits)
	}

	AllowInsecureRemoteScorerHTTP = true
	got, err := scoreRemote(nil, context.Background(), "e/p", val, map[string]any{"q": 1}, "out", nil, nil)
	if err != nil {
		t.Fatalf("http url with opt-in: %v", err)
	}
	if m, ok := got.(map[string]any); !ok || m["score"] == nil {
		t.Fatalf("unexpected score %#v", got)
	}
}
