package weftpush

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/models"
)

// Weft can recover from an outage between a record's last 500 and the probe
// that follows it. The probe then succeeds, which used to mark the record as
// one Weft cannot store and drop it; it must be sent once more instead.
func TestPusher_RecoveryDuringProbeDoesNotDropRecord(t *testing.T) {
	f := newFakeWeft()
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if down.Load() {
			// The probe re-sends the first stored detection on its own: Weft
			// is back exactly when it arrives.
			if bytes.Contains(body, []byte(`"first"`)) && !bytes.Contains(body, []byte(`"only"`)) {
				down.Store(false)
			} else {
				http.Error(w, "server error", http.StatusInternalServerError)
				return
			}
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	p.Backoff = func(int) time.Duration { return 5 * time.Millisecond }
	p.MaxRetry = time.Minute
	p.Start(context.Background())
	p.Verdict(verdict("first", models.ActionBlock, blockRule()))
	waitFor(t, func() bool { return p.Stats().Sent == 1 })
	down.Store(true)
	p.Verdict(verdict("only", models.ActionBlock, blockRule()))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waitFor(t, func() bool { st := p.Stats(); return st.Pending == 0 })
	p.Close(ctx)
	st := p.Stats()
	checkBalance(t, st)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.stored["only"]; !ok || st.Failed != 0 || st.Sent != 2 {
		t.Fatalf("record dropped after Weft recovered during the probe: stored=%v stats=%+v", len(f.stored), st)
	}
}
