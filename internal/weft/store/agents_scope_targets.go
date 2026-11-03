package store

// Scope targets for scope scorers (docs/weft/DESIGN.md §6): the text a
// session, agent or hand-off scorer sees, rendered from the session
// timeline.
//
//   - Session scorers see the user's request and the whole timeline.
//   - Agent scorers see the task the agent was given and its own turns,
//     reasoning, tool calls and hand-offs within the session.
//   - Hand-off scorers see the delegating context, the hand-off input, the
//     delegate's output and what the caller did next.

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// ScopeTarget is one thing to score, with the judge's input and output text.
type ScopeTarget struct {
	Scope     string `json:"scope"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	HandoffID string `json:"handoff_id,omitempty"`
	// Input is the request or context (the judge's {input}); Output is what
	// is being judged (the judge's {output}).
	Input  string `json:"input"`
	Output string `json:"output"`
}

// ScopeTargetsReq selects targets.
type ScopeTargetsReq struct {
	ProjectID  string
	Scope      string
	SessionIDs []string
	Agent      string
	HandoffIDs []string
}

const (
	maxTranscript     = 16000
	maxTranscriptLine = 1500
)

func clip(s string, n int) string { return truncateText(strings.ReplaceAll(s, "\r", ""), n) }

func payloadStrAny(p map[string]any, k string) string {
	switch v := p[k].(type) {
	case string:
		return v
	case *string:
		if v != nil {
			return *v
		}
	}
	return ""
}

// renderItem is one transcript line.
func renderItem(it *TimelineItem) string {
	p := it.Payload
	who := it.AgentName
	if who == "" {
		who = "system"
	}
	var line string
	switch it.Type {
	case "turn":
		return "--- turn ---"
	case "message":
		role := payloadStrAny(p, "role")
		if role == "assistant" && it.AgentName != "" {
			role = it.AgentName + " (final answer)"
		}
		line = role + ": " + payloadStrAny(p, "text")
	case "reasoning":
		line = fmt.Sprintf("[%s] reasoning (%s): %s", who, payloadStrAny(p, "kind"), payloadStrAny(p, "text"))
	case "llm":
		out := payloadStrAny(p, "output_preview")
		var calls []string
		if tcs, ok := p["tool_calls"].([]any); ok {
			for _, x := range tcs {
				if m := asMap(x); m != nil {
					calls = append(calls, fmt.Sprintf("%v(%v)", m["name"], m["arguments"]))
				}
			}
		}
		if out == "" && len(calls) == 0 {
			return ""
		}
		line = fmt.Sprintf("[%s] model says: %s", who, out)
		if len(calls) > 0 {
			line += " | requests: " + strings.Join(calls, "; ")
		}
	case "tool_call":
		status := ""
		if payloadStrAny(p, "status") == "ERROR" {
			status = " (failed)"
		}
		line = fmt.Sprintf("[%s] tool %s(%s)%s -> %s", who, payloadStrAny(p, "tool_name"), clip(payloadStrAny(p, "arguments"), 400), status,
			payloadStrAny(p, "result"))
	case "handoff":
		from, to, kind := payloadStrAny(p, "from_agent"), payloadStrAny(p, "to_agent"), payloadStrAny(p, "kind")
		if kind == HandoffReturn {
			line = fmt.Sprintf("[%s] returns control to %s: %s", from, to, payloadStrAny(p, "output_preview"))
		} else {
			line = fmt.Sprintf("[%s] hands off to %s (%s): %s", from, to, kind, payloadStrAny(p, "input_preview"))
		}
	case "error":
		line = fmt.Sprintf("[%s] error: %s", who, payloadStrAny(p, "message"))
	default:
		return ""
	}
	return clip(line, maxTranscriptLine)
}

func renderTranscript(items []*TimelineItem, keep func(*TimelineItem) bool) string {
	var lines []string
	for _, it := range items {
		if keep != nil && !keep(it) {
			continue
		}
		if l := renderItem(it); l != "" {
			lines = append(lines, l)
		}
	}
	s := strings.Join(lines, "\n")
	if len(s) <= maxTranscript {
		return s
	}
	head, tail := s[:maxTranscript/4], s[len(s)-maxTranscript*3/4:]
	return strings.ToValidUTF8(head, "") + fmt.Sprintf("\n… (%d characters omitted) …\n", len(s)-len(head)-len(tail)) + strings.ToValidUTF8(tail, "")
}

func firstUserRequest(items []*TimelineItem) string {
	var reqs []string
	for _, it := range items {
		if it.Type == "message" && payloadStrAny(it.Payload, "role") == "user" {
			if t := payloadStrAny(it.Payload, "text"); t != "" {
				reqs = append(reqs, t)
			}
		}
	}
	if len(reqs) == 0 {
		return "(no user request was recorded)"
	}
	return clip(strings.Join(reqs, "\n---\n"), 4000)
}

func involves(it *TimelineItem, agent string) bool {
	if it.AgentName == agent {
		return true
	}
	if it.Type == "handoff" {
		return payloadStrAny(it.Payload, "from_agent") == agent || payloadStrAny(it.Payload, "to_agent") == agent
	}
	return false
}

// ScopeTargets builds the targets of a scope run.
func (s *Store) ScopeTargets(ctx context.Context, req ScopeTargetsReq) ([]ScopeTarget, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	timelines := map[string][]*TimelineItem{}
	timeline := func(sid string) ([]*TimelineItem, error) {
		if t, ok := timelines[sid]; ok {
			return t, nil
		}
		t, err := s.sessionTimeline(ctx, pid, sid)
		if err == ErrNotFound {
			return nil, Invalidf("unknown session %q", sid)
		}
		if err != nil {
			return nil, err
		}
		timelines[sid] = t
		return t, nil
	}
	var out []ScopeTarget
	switch req.Scope {
	case ScopeSession:
		for _, sid := range req.SessionIDs {
			items, err := timeline(sid)
			if err != nil {
				return nil, err
			}
			out = append(out, ScopeTarget{Scope: ScopeSession, SessionID: sid, Input: firstUserRequest(items), Output: renderTranscript(items, nil)})
		}
	case ScopeAgent:
		for _, sid := range req.SessionIDs {
			items, err := timeline(sid)
			if err != nil {
				return nil, err
			}
			var agents []string
			for _, it := range items {
				for _, a := range []string{it.AgentName, payloadStrAny(it.Payload, "from_agent"), payloadStrAny(it.Payload, "to_agent")} {
					if a != "" && !containsStr(agents, a) && (req.Agent == "" || a == req.Agent) {
						agents = append(agents, a)
					}
				}
			}
			for _, a := range agents {
				task := ""
				for _, it := range items {
					if it.Type == "handoff" && payloadStrAny(it.Payload, "to_agent") == a && payloadStrAny(it.Payload, "kind") != HandoffReturn {
						task = payloadStrAny(it.Payload, "input_preview")
						break
					}
				}
				if task == "" {
					task = firstUserRequest(items)
				}
				out = append(out, ScopeTarget{Scope: ScopeAgent, SessionID: sid, Agent: a, Input: task,
					Output: renderTranscript(items, func(it *TimelineItem) bool { return involves(it, a) })})
			}
		}
	case ScopeHandoff:
		q := HandoffsQuery{SessionIDs: req.SessionIDs}
		if len(req.HandoffIDs) > 0 {
			q.SessionIDs = nil
		}
		edges, err := s.loadHandoffs(ctx, pid, q)
		if err != nil {
			return nil, err
		}
		want := map[string]bool{}
		for _, id := range req.HandoffIDs {
			want[id] = true
		}
		for _, h := range edges {
			if h.Kind == HandoffReturn || (len(want) > 0 && !want[h.ID]) {
				continue
			}
			delete(want, h.ID)
			items, err := timeline(h.SessionID)
			if err != nil {
				return nil, err
			}
			end := h.startUS
			if h.hasEnd {
				end = h.endUS
			}
			before := renderTranscript(items, func(it *TimelineItem) bool {
				return it.startUS < h.startUS && (it.AgentName == h.FromAgent || (it.Type == "message" && payloadStrAny(it.Payload, "role") == "user"))
			})
			var after []*TimelineItem
			for _, it := range items {
				if it.startUS >= end && involves(it, h.FromAgent) && !(it.Type == "handoff" && payloadStrAny(it.Payload, "handoff_id") == h.ID) {
					after = append(after, it)
				}
			}
			if len(after) > 40 {
				after = after[:40]
			}
			result := h.OutputPreview
			if result == "" {
				result = "(the delegate returned nothing that was recorded)"
			}
			next := renderTranscript(after, nil)
			if next == "" {
				next = "(nothing more was recorded for " + h.FromAgent + ")"
			}
			out = append(out, ScopeTarget{Scope: ScopeHandoff, SessionID: h.SessionID, HandoffID: h.ID,
				Input: fmt.Sprintf("User request:\n%s\n\nWhat %s did before the hand-off:\n%s\n\nHand-off from %s to %s (%s) with this input:\n%s",
					firstUserRequest(items), h.FromAgent, before, h.FromAgent, h.ToAgent, h.Kind, h.InputPreview),
				Output: fmt.Sprintf("What %s returned:\n%s\n\nWhat %s did next:\n%s", h.ToAgent, result, h.FromAgent, next)})
		}
		for id := range want {
			return nil, Invalidf("unknown handoff_id %q", id)
		}
	default:
		return nil, Invalidf("scores/run supports the session, agent and handoff scopes")
	}
	return out, nil
}
