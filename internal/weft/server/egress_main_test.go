package server

import (
	"os"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
)

// TestMain allows the loopback httptest servers these tests talk to through
// the egress allowlist, the way an operator would (--egress-allow), instead
// of weakening the default policy. Tests of the default policy install it
// with egress.SetDefault and restore this one afterwards.
func TestMain(m *testing.M) {
	egress.SetDefault(egress.MustNew(egress.Config{Allow: []string{"127.0.0.1", "::1", "localhost"}}))
	os.Exit(m.Run())
}
