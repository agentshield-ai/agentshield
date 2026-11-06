package otlp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func TestZZDebugDump(t *testing.T) {
	dir := os.Getenv("DUMP_DIR")
	if dir == "" {
		t.Skip()
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, d := range strings.Split(dir, ",") {
		files, _ := filepath.Glob(filepath.Join(d, "*_logs.*"))
		sort.Strings(files)
		for _, f := range files {
			b, _ := os.ReadFile(f)
			req, err := DecodeLogs(b, strings.HasSuffix(f, ".json"))
			if err != nil {
				t.Fatal(err)
			}
			sl, err := SessionLogEvents(req, "dbg/x")
			if err != nil {
				t.Fatal(err)
			}
			for _, src := range LogSources {
				for pid, evs := range sl.Events[src.Name] {
					if _, _, err := s.IngestLogEvents(context.Background(), pid, src, evs); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	calls, err := s.ListCalls(context.Background(), store.CallsQuery{ProjectID: "dbg/x", IncludeCosts: true, IncludeRunStatus: true})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].StartedAt < calls[j].StartedAt })
	byID := map[string]*store.Call{}
	for _, c := range calls {
		byID[c.ID] = c
	}
	depth := func(c *store.Call) int {
		d := 0
		for c.ParentID != nil && byID[*c.ParentID] != nil {
			c = byID[*c.ParentID]
			d++
		}
		return d
	}
	for _, c := range calls {
		exc := ""
		if c.Exception != nil {
			exc = " EXC=" + *c.Exception
		}
		end := "open"
		if c.EndedAt != nil {
			end = "ended"
		}
		w, _ := c.Summary["weft"].(map[string]any)
		req, p, comp, _, _, cost := usage(c)
		fmt.Printf("%s%s [%s] sess=%.8s turn=%.8s %s run=%v req=%v p=%v c=%v $%.5f tcid=%v%s\n", strings.Repeat("  ", depth(c)), deref(c.DisplayName), c.OpName, deref(c.ThreadID), deref(c.TurnID), end, w["run_status"], req, p, comp, cost, attr(c, "gen_ai.tool.call.id"), exc)
	}
}
