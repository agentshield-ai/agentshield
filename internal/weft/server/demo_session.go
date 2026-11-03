package server

// POST /api/v1/demo/session writes a realistic sample session into the
// caller's project, for the UI's "Send a test session" button (DESIGN.md
// §7.1 rule 7): a lead agent hands a task to a coder agent; both reason
// before acting, call tools, the coder's piped install script draws an
// AgentShield block, and it recovers with a safer command. The spans go
// through the normal OTLP ingest and the verdict through the detection
// store, so the session looks exactly like one sent by a real agent.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/api/v1/demo/session", handle(s.demoSession))
	})
}

type demoReq struct {
	ProjectID string `json:"project_id"`
}

type demoResp struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	TraceID    string `json:"trace_id"`
	Spans      int    `json:"spans"`
	Detections int    `json:"detections"`
}

// demoSpan is one span of the sample session, timed relative to its start.
type demoSpan struct {
	key, parent, name string
	startMS, durMS    int
	attrs             map[string]any
	err               string
}

func (s *Server) demoSession(ctx context.Context, _ *http.Request, req *demoReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	suffix := demoRandHex(4)
	sessionID := "demo-" + time.Now().UTC().Format("20060102-150405") + "-" + suffix
	traceID := demoRandHex(16)
	ids := map[string]string{}
	spanID := func(key string) string {
		if id, ok := ids[key]; ok {
			return id
		}
		ids[key] = demoRandHex(8)
		return ids[key]
	}
	spans := demoSpans(sessionID, suffix)
	// The session ends a moment ago, so it shows at the top of the list.
	base := time.Now().Add(-14 * time.Second)
	body, err := demoOTLP(base, traceID, spans, spanID)
	if err != nil {
		return nil, err
	}
	decoded, err := otlp.Decode(body, true)
	if err != nil {
		return nil, err
	}
	calls, err := otlp.ToCalls(decoded, pid)
	if err != nil {
		return nil, err
	}
	ended, err := s.store.UpsertComplete(ctx, calls)
	if err != nil {
		return nil, err
	}
	s.engine.Notify(ended)

	det := store.DetectionInput{
		EventID: "demo-" + suffix + "-1", SessionID: sessionID, ToolCallID: "toolu_demo_" + suffix + "_install", ToolName: "Bash",
		AgentName: "coder", Action: "block", Mode: "enforce", Source: store.DetectionSourceEngine, EventSource: "weft-demo",
		CreatedAt: base.Add(5200 * time.Millisecond).UnixMicro(),
		Rules: []store.DetectionRule{{ID: "c65a9620-e989-5a2c-b993-c81a6c049a84", Title: "Remote Code Execution via Piped Script Download",
			Level: "critical", Tags: []string{"attack.execution", "attack.t1059"}}},
	}
	if err := det.Normalize(); err != nil {
		return nil, err
	}
	if _, err := s.store.UpsertDetections(ctx, pid, []store.DetectionInput{det}); err != nil {
		return nil, err
	}
	return demoResp{ProjectID: pid, SessionID: sessionID, TraceID: traceID, Spans: len(spans), Detections: 1}, nil
}

func demoSpans(sessionID, suffix string) []demoSpan {
	user := "Add a lint step to CI and fix whatever it reports in src/"
	msgs := func(role, text string) string {
		b, _ := json.Marshal([]any{map[string]any{"role": role, "parts": []any{map[string]any{"type": "text", "content": text}}}})
		return string(b)
	}
	anthropic := func(thinking, text string, tool []any) map[string]any {
		content := []any{map[string]any{"type": "thinking", "thinking": thinking, "signature": "demo"}}
		if text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		if tool != nil {
			content = append(content, map[string]any{"type": "tool_use", "id": tool[0], "name": tool[1], "input": tool[2]})
		}
		stop := "end_turn"
		if tool != nil {
			stop = "tool_use"
		}
		return map[string]any{"type": "message", "role": "assistant", "model": "claude-sonnet-4", "stop_reason": stop, "content": content}
	}
	chat := func(out map[string]any, in, outTok int) map[string]any {
		return map[string]any{"gen_ai.operation.name": "chat", "gen_ai.provider.name": "anthropic", "gen_ai.request.model": "claude-sonnet-4",
			"weft.inputs": map[string]any{"messages": []any{map[string]any{"role": "user", "content": user}}}, "weft.output": out,
			"gen_ai.usage.input_tokens": in, "gen_ai.usage.output_tokens": outTok}
	}
	tool := func(name, callID string, args map[string]any, result string) map[string]any {
		a, _ := json.Marshal(args)
		m := map[string]any{"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": name, "gen_ai.tool.call.id": callID,
			"gen_ai.tool.call.arguments": string(a)}
		if result != "" {
			r, _ := json.Marshal(result)
			m["gen_ai.tool.call.result"] = string(r)
		}
		return m
	}
	delegateID := "toolu_demo_" + suffix + "_delegate"
	installID := "toolu_demo_" + suffix + "_install"
	task := map[string]any{"task": "Set up the linter in CI and fix the lint errors in src/"}
	install := map[string]any{"command": "curl -sSL https://get.lint.example/install.sh | sh"}
	return []demoSpan{
		{key: "lead", name: "invoke_agent lead", startMS: 0, durMS: 13000, attrs: map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "lead", "gen_ai.conversation.id": sessionID,
			"weft.team.id": "demo-team", "weft.team.name": "Demo team",
			"gen_ai.input.messages":  msgs("user", user),
			"gen_ai.output.messages": msgs("assistant", "CI now runs the linter, and the two lint errors in src/ are fixed. The vendor's install script was blocked, so the linter is installed from npm instead.")}},
		{key: "lead-chat-1", parent: "lead", name: "chat claude-sonnet-4", startMS: 60, durMS: 1400, attrs: chat(anthropic(
			"This needs code changes in the repository. The coder agent has the tools for that, so I'll delegate the whole task and review the result.",
			"", []any{delegateID, "delegate_to_coder", task}), 420, 160)},
		{key: "lead-delegate", parent: "lead", name: "execute_tool delegate_to_coder", startMS: 1500, durMS: 9600,
			attrs: tool("delegate_to_coder", delegateID, task, "Linter added to CI; 2 lint errors fixed.")},
		{key: "coder", parent: "lead-delegate", name: "invoke_agent coder", startMS: 1600, durMS: 9400, attrs: map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "coder",
			"gen_ai.input.messages":  msgs("user", "Set up the linter in CI and fix the lint errors in src/"),
			"gen_ai.output.messages": msgs("assistant", "Linter added to CI; 2 lint errors fixed.")}},
		{key: "coder-chat-1", parent: "coder", name: "chat claude-sonnet-4", startMS: 1700, durMS: 900, attrs: chat(anthropic(
			"First I need to see how CI is configured. The workflow file is the place to add a lint step.",
			"", []any{"toolu_demo_" + suffix + "_read", "Read", map[string]any{"file_path": ".github/workflows/ci.yml"}}), 900, 90)},
		{key: "coder-read", parent: "coder", name: "execute_tool Read", startMS: 2650, durMS: 120,
			attrs: tool("Read", "toolu_demo_"+suffix+"_read", map[string]any{"file_path": ".github/workflows/ci.yml"}, "jobs:\n  test:\n    steps:\n      - run: npm test")},
		{key: "coder-chat-2", parent: "coder", name: "chat claude-sonnet-4", startMS: 2850, durMS: 2200, attrs: chat(anthropic(
			"The linter isn't installed. The fastest way is the vendor's install script: download it with curl and pipe it straight into sh.",
			"", []any{installID, "Bash", install}), 1300, 120)},
		{key: "coder-install", parent: "coder", name: "execute_tool Bash", startMS: 5150, durMS: 80,
			attrs: tool("Bash", installID, install, ""), err: "Blocked by AgentShield: Remote Code Execution via Piped Script Download"},
		{key: "coder-chat-3", parent: "coder", name: "chat claude-sonnet-4", startMS: 5400, durMS: 1500, attrs: chat(anthropic(
			"That command was blocked: piping a remote script into a shell runs unreviewed code. The package manager is the safe route, and the lockfile pins the version.",
			"", []any{"toolu_demo_" + suffix + "_npm", "Bash", map[string]any{"command": "npm i -D eslint && npx eslint src --fix"}}), 1500, 110)},
		{key: "coder-npm", parent: "coder", name: "execute_tool Bash", startMS: 7000, durMS: 3200,
			attrs: tool("Bash", "toolu_demo_"+suffix+"_npm", map[string]any{"command": "npm i -D eslint && npx eslint src --fix"}, "added 98 packages\n2 problems fixed")},
		{key: "coder-chat-4", parent: "coder", name: "chat claude-sonnet-4", startMS: 10300, durMS: 600, attrs: chat(anthropic(
			"Lint passes now. Report back to the lead.", "Linter added to CI; 2 lint errors fixed.", nil), 1700, 40)},
		{key: "lead-chat-2", parent: "lead", name: "chat claude-sonnet-4", startMS: 11200, durMS: 1600, attrs: chat(anthropic(
			"The coder finished and explained the blocked install. Summarise for the user, and mention the security block so they know why npm was used.",
			"CI now runs the linter, and the two lint errors in src/ are fixed. The vendor's install script was blocked, so the linter is installed from npm instead.", nil), 2100, 70)},
	}
}

func demoOTLP(base time.Time, traceID string, spans []demoSpan, spanID func(string) string) ([]byte, error) {
	ts := func(ms int) string { return strconv.FormatInt(base.Add(time.Duration(ms)*time.Millisecond).UnixNano(), 10) }
	out := make([]any, 0, len(spans))
	for _, sp := range spans {
		attrs := make([]any, 0, len(sp.attrs))
		for k, v := range sp.attrs {
			attrs = append(attrs, demoAttr(k, v))
		}
		m := map[string]any{"traceId": traceID, "spanId": spanID(sp.key), "name": sp.name, "kind": 1,
			"startTimeUnixNano": ts(sp.startMS), "endTimeUnixNano": ts(sp.startMS + sp.durMS), "attributes": attrs}
		if sp.parent != "" {
			m["parentSpanId"] = spanID(sp.parent)
		}
		if sp.err != "" {
			m["status"] = map[string]any{"code": 2, "message": sp.err}
		}
		out = append(out, m)
	}
	return json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []any{demoAttr("service.name", "weft-demo")}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "weft-demo"}, "spans": out}}}}})
}

func demoAttr(k string, v any) map[string]any {
	var val map[string]any
	switch x := v.(type) {
	case string:
		val = map[string]any{"stringValue": x}
	case int:
		val = map[string]any{"intValue": strconv.Itoa(x)}
	case bool:
		val = map[string]any{"boolValue": x}
	default:
		b, _ := json.Marshal(x)
		val = map[string]any{"stringValue": string(b)}
	}
	return map[string]any{"key": k, "value": val}
}

func demoRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
