package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/models"
)

// The engine loads rules whatever their ids and tags hold and blocks on them,
// so Weft cleans those values rather than rejecting the verdict: tags are
// cleaned, cut and at most 64 kept, and an over-long rule id becomes a stable
// prefix + hash stand-in. Both the push and the OTLP path store such
// verdicts, and they agree on the rule id. A record that is invalid is
// reported on its own; the rest of the batch is stored.
func TestShieldIngestCleansRuleValuesAndReportsBadRecords(t *testing.T) {
	e := newShieldEnv(t, "")
	longID := strings.Repeat("i", 300)
	manyTags := []any{}
	for i := 0; i < 70; i++ {
		manyTags = append(manyTags, fmt.Sprintf("t%d", i))
	}
	det := func(id string, rule map[string]any) map[string]any {
		return map[string]any{"event_id": id, "session_id": "s-clean", "tool_name": "Bash", "action": "block", "mode": "enforce",
			"rules": []any{rule}}
	}
	body := shieldIngestBody(
		det("tab", map[string]any{"id": "c14tag-tab", "level": "high", "tags": []any{"attack.x\tfoo"}}),
		det("longtag", map[string]any{"id": "c14tag-long", "level": "high", "tags": []any{"attack." + strings.Repeat("z", 300)}}),
		det("many", map[string]any{"id": "c14tag-many", "level": "high", "tags": manyTags}),
		map[string]any{"event_id": "bad", "session_id": "s-clean", "action": "deny"},
		det("longid", map[string]any{"id": longID, "level": "high", "tags": []any{"attack.y"}}),
	)
	code, out := e.do(http.MethodPost, "/api/v1/shield/ingest", body, "")
	errs, _ := out["errors"].([]any)
	if code != http.StatusOK || out["accepted"].(float64) != 4 || out["rejected"].(float64) != 1 || len(errs) != 1 {
		t.Fatalf("ingest: %d %v", code, out)
	}
	if e0 := errs[0].(map[string]any); e0["index"].(float64) != 3 || e0["event_id"] != "bad" || e0["kind"] != "detection" ||
		!strings.Contains(e0["error"].(string), "action") {
		t.Errorf("error = %v", e0)
	}
	byEvent := map[string]map[string]any{}
	for _, d := range shieldItems(e.get("/api/v1/detections", url.Values{"session_id": {"s-clean"}})) {
		byEvent[d["event_id"].(string)] = d
	}
	rule := func(id string) map[string]any { return byEvent[id]["rules"].([]any)[0].(map[string]any) }
	if len(byEvent) != 4 {
		t.Fatalf("stored %d of 4: %v", len(byEvent), byEvent)
	}
	if tags := rule("tab")["tags"].([]any); tags[0] != "attack.x foo" {
		t.Errorf("tab tags = %v", tags)
	}
	if tags := rule("longtag")["tags"].([]any); len(tags[0].(string)) != 256 {
		t.Errorf("long tag = %d bytes", len(tags[0].(string)))
	}
	if tags := rule("many")["tags"].([]any); len(tags) != 64 || tags[63] != "t63" {
		t.Errorf("many tags = %d", len(tags))
	}
	short := models.ShortID(longID, 256)
	if rule("longid")["id"] != short {
		t.Errorf("long rule id = %v, want %s", rule("longid")["id"], short)
	}

	// The same verdict over OTLP: an engine that predates the cleaning
	// sends the raw values; the detection is stored with the same rule id.
	eval := shieldOTLPSpan(shieldEngineTrace, "a9a2a3a4a5a6a7a8", "", "evaluate", time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC), map[string]any{
		"event.id": "otel-longid", "gen_ai.conversation.id": "s-clean-otel", "gen_ai.tool.name": "Bash",
		"agentshield.verdict.action": "block", "agentshield.verdict.mode": "enforce", "agentshield.rule_ids": []string{longID},
	}, map[string]any{"name": "agentshield.rule.matched", "attributes": map[string]any{"rule.id": longID, "rule.title": "t",
		"rule.level": "high", "rule.tags": []string{"attack.x\tfoo", strings.Repeat("q", 400)}}})
	if code, out := e.do(http.MethodPost, "/otel/v1/traces", shieldOTLPBody("agentshield", eval), "application/json"); code != http.StatusOK {
		t.Fatalf("otlp: %d %v", code, out)
	}
	d := shieldItems(e.get("/api/v1/detections", url.Values{"session_id": {"s-clean-otel"}}))
	if len(d) != 1 {
		t.Fatalf("OTLP detection not stored: %v", d)
	}
	r := d[0]["rules"].([]any)[0].(map[string]any)
	if r["id"] != short || r["tags"].([]any)[0] != "attack.x foo" || len(r["tags"].([]any)[1].(string)) != 256 {
		t.Errorf("OTLP rule = %v", r)
	}
}
