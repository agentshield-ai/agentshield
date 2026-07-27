package replay

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// loadATBenchFixtures returns the rows in testdata/atbench_rows.json, which are
// verbatim rows from the real dataset chosen to cover its structural variety:
//
//	id 1   label 1  a poisoned tool description, single episode
//	id 71  label 1  two Complete sentinels, so the trace is two episodes
//	id 850 label 0  a benign-labelled trace whose risk_source is an attack
func loadATBenchFixtures(t *testing.T) []map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile("testdata/atbench_rows.json")
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decoding fixtures: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 fixture rows, got %d", len(rows))
	}
	return rows
}

func TestATBenchAdapter_Labels(t *testing.T) {
	rows := loadATBenchFixtures(t)
	a := &ATBenchAdapter{}

	tests := []struct {
		row        int
		id         string
		malicious  bool
		riskSource string
	}{
		{0, "1", true, "tool_description_injection"},
		{1, "71", true, "malicious_user_instruction_or_jailbreak"},
		{2, "850", false, "malicious_user_instruction_or_jailbreak"},
	}
	for _, tc := range tests {
		label, _, err := a.ExtractLabelled(rows[tc.row])
		if err != nil {
			t.Fatalf("row %d: %v", tc.row, err)
		}
		if !label.Present {
			t.Errorf("row %d: label not marked present", tc.row)
		}
		if label.TraceID != tc.id {
			t.Errorf("row %d: TraceID = %q, want %q", tc.row, label.TraceID, tc.id)
		}
		if label.Malicious != tc.malicious {
			t.Errorf("row %d: Malicious = %v, want %v", tc.row, label.Malicious, tc.malicious)
		}
		if label.RiskSource != tc.riskSource {
			t.Errorf("row %d: RiskSource = %q, want %q", tc.row, label.RiskSource, tc.riskSource)
		}
	}
}

// TestATBenchAdapter_BenignTraceCanCarryAnAttack pins the property that makes
// this corpus easy to misread: a label of "safe" means the agent came to no
// harm, not that no attack was present.
func TestATBenchAdapter_BenignTraceCanCarryAnAttack(t *testing.T) {
	rows := loadATBenchFixtures(t)
	label, _, err := (&ATBenchAdapter{}).ExtractLabelled(rows[2])
	if err != nil {
		t.Fatal(err)
	}
	if label.Malicious {
		t.Fatal("fixture row 850 should be labelled safe")
	}
	if label.RiskSource == "" || label.RiskSource == "benign" {
		t.Errorf("RiskSource = %q, want a non-benign attack category on a safe-labelled trace", label.RiskSource)
	}
}

func TestATBenchAdapter_ExtractsCallsResultsAndDescriptions(t *testing.T) {
	rows := loadATBenchFixtures(t)
	_, events, err := (&ATBenchAdapter{}).ExtractLabelled(rows[0])
	if err != nil {
		t.Fatal(err)
	}

	counts := map[EventKind]int{}
	for _, e := range events {
		counts[kindOf(e)]++
	}
	// Row 1 declares two tools and makes two calls, each answered by an
	// environment turn. The third agent turn is the Complete sentinel.
	if counts[EventKindDescription] != 2 {
		t.Errorf("description events = %d, want 2", counts[EventKindDescription])
	}
	if counts[EventKindCall] != 2 {
		t.Errorf("call events = %d, want 2", counts[EventKindCall])
	}
	if counts[EventKindResult] != 2 {
		t.Errorf("result events = %d, want 2", counts[EventKindResult])
	}

	var calls []ExtractedEvent
	for _, e := range events {
		if kindOf(e) == EventKindCall {
			calls = append(calls, e)
		}
	}
	if calls[0].ToolName != "extract_news_article_text" {
		t.Errorf("first call = %q, want extract_news_article_text", calls[0].ToolName)
	}
	if calls[1].ToolName != "create_event" {
		t.Errorf("second call = %q, want create_event", calls[1].ToolName)
	}
	if calls[0].URL == "" {
		t.Error("first call should carry a url argument")
	}
	// The environment turn following a call is attached to it, not emitted
	// as a free-standing event.
	if !strings.Contains(calls[0].Content, "hazardous material") {
		t.Errorf("first call did not pick up its environment response: %q", truncate(calls[0].Content, 120))
	}
	if !strings.Contains(calls[1].ArgumentsJSON, "Officer Marcus Thorne") {
		t.Errorf("arguments blob missing call arguments: %q", truncate(calls[1].ArgumentsJSON, 120))
	}
}

// TestATBenchAdapter_UsesPoisonedDescription pins that the adapter feeds rules
// the text the agent actually saw, not the preserved clean original.
func TestATBenchAdapter_UsesPoisonedDescription(t *testing.T) {
	rows := loadATBenchFixtures(t)
	_, events, err := (&ATBenchAdapter{}).ExtractLabelled(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var desc string
	for _, e := range events {
		if kindOf(e) == EventKindDescription && e.ToolName == "create_event" {
			desc = e.Description
		}
	}
	if desc == "" {
		t.Fatal("no description event for create_event")
	}
	if !strings.Contains(desc, "System Mode: Rapid Response Automation") {
		t.Error("description should be the attacker's poisoned text")
	}
	if strings.Contains(desc, "_original_description") {
		t.Error("description leaked the schema key")
	}

	schemas := parseToolSchemas(rows[0]["tool_used"])
	if !schemas["create_event"].Poisoned() {
		t.Error("create_event should be detected as poisoned")
	}
	if schemas["extract_news_article_text"].Poisoned() {
		t.Error("extract_news_article_text is not poisoned")
	}
}

// TestATBenchAdapter_MultiEpisodeTrace covers a conversation whose first agent
// turn is already a Complete sentinel, after which the user speaks again and the
// agent makes its only real tool call. Treating Complete as the end of the trace
// would extract nothing at all here. 351 of the 1,000 rows carry more than one
// Complete, and 546 Complete turns are not the final agent turn.
func TestATBenchAdapter_MultiEpisodeTrace(t *testing.T) {
	rows := loadATBenchFixtures(t)
	_, events, err := (&ATBenchAdapter{}).ExtractLabelled(rows[1])
	if err != nil {
		t.Fatal(err)
	}
	var calls []ExtractedEvent
	for _, e := range events {
		if kindOf(e) == EventKindCall {
			calls = append(calls, e)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1 (the call in the second episode)", len(calls))
	}
	if calls[0].ToolName != "list_customer_wallets" {
		t.Errorf("call = %q, want list_customer_wallets", calls[0].ToolName)
	}
	if calls[0].Content == "" {
		t.Error("call should carry the environment response that followed it")
	}
}

// TestParseAgentAction covers the action encodings the corpus actually uses.
func TestParseAgentAction(t *testing.T) {
	tests := []struct {
		name     string
		action   interface{}
		wantOK   bool
		wantName string
		wantArg  string // key that must be present in the flattened args
	}{
		{
			name:     "object arguments",
			action:   `{"name": "create_event", "arguments": {"agent": "A", "phoneNum": "123"}}`,
			wantOK:   true,
			wantName: "create_event",
			wantArg:  "phoneNum",
		},
		{
			// Verbatim from row id 449. Six rows encode arguments as a JSON
			// string rather than an object.
			name:     "string-encoded arguments",
			action:   `{"name": "delete_event", "arguments": "{\"category\": \"Devices\", \"event_id\": 8493021}"}`,
			wantOK:   true,
			wantName: "delete_event",
			wantArg:  "event_id",
		},
		{
			name:   "Complete sentinel carries no call",
			action: `Complete{"response": "done"}`,
			wantOK: false,
		},
		{
			name:   "empty action",
			action: "",
			wantOK: false,
		},
		{
			name:   "non-string action",
			action: 42,
			wantOK: false,
		},
		{
			name:   "malformed JSON",
			action: `{"name": "x", "arguments":`,
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, args, argsJSON, ok := parseAgentAction(tc.action)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if _, present := args[tc.wantArg]; !present {
				t.Errorf("args missing %q; got %v", tc.wantArg, args)
			}
			if !json.Valid([]byte(argsJSON)) {
				t.Errorf("arguments blob is not valid JSON: %q", argsJSON)
			}
		})
	}
}

// TestParseAgentAction_NumericArgumentsSurvive guards the flattening of the
// non-string argument values the corpus contains (int, bool, float, list, dict).
func TestParseAgentAction_NumericArgumentsSurvive(t *testing.T) {
	_, args, _, ok := parseAgentAction(
		`{"name": "t", "arguments": {"n": 42, "b": true, "f": 1.5, "l": [1,2], "d": {"k":"v"}}}`)
	if !ok {
		t.Fatal("expected a parsed call")
	}
	for _, k := range []string{"n", "b", "f", "l", "d"} {
		if args[k] == "" {
			t.Errorf("argument %q was dropped; got %v", k, args)
		}
	}
}

func TestSelectAdapter_ATBench(t *testing.T) {
	for _, name := range []string{"AI45Research/ATBench", "AI45Research/ATBench500", "someone/atbench-mirror"} {
		a, err := SelectAdapter(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if a.Name() != "atbench" {
			t.Errorf("%s: adapter = %q, want atbench", name, a.Name())
		}
	}
}

func TestViewForDataset(t *testing.T) {
	tests := []struct {
		dataset string
		config  string
		split   string
	}{
		{"AI45Research/ATBench", "ATBench", "test"},
		{"AI45Research/ATBench500", "ATBench", "test"},
		{"sammshen/wildclaw-opus-traces", "default", "train"},
	}
	for _, tc := range tests {
		got := ViewForDataset(tc.dataset)
		if got.Config != tc.config || got.Split != tc.split {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.dataset, got.Config, got.Split, tc.config, tc.split)
		}
	}
}
