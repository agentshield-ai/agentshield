package server

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Native API (/api/v1, docs/weft/DESIGN.md §3): sessions, agents, teams,
// hand-offs, reasoning and scope scores. GET requests take project_id in
// the query, POST requests in the body; lists take limit, offset and
// sort_by ("field", "-field" or "field:desc") and return {items, total_count}.
// Authorisation is the server's usual project-role check (iam.go), which
// reads project_id from the query or body.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/api/v1/sessions", s.v1Sessions)
		r.Get("/api/v1/sessions/attention", s.v1SessionsAttention)
		r.Get("/api/v1/sessions/{id}", s.v1Session)
		r.Get("/api/v1/sessions/{id}/timeline", s.v1SessionTimeline)
		r.Get("/api/v1/sessions/{id}/graph", s.v1SessionGraph)
		r.Get("/api/v1/sessions/{id}/reasoning", s.v1SessionReasoning)
		r.Get("/api/v1/agents", s.v1Agents)
		r.Get("/api/v1/agents/{name}", s.v1Agent)
		r.Get("/api/v1/teams", s.v1Teams)
		r.Get("/api/v1/teams/{id}", s.v1Team)
		r.Get("/api/v1/handoffs", s.v1Handoffs)
		r.Get("/api/v1/scores", s.v1Scores)
		r.Post("/api/v1/scores/run", handle(s.v1ScoresRun))
	})
}

// v1Req holds the common query parameters.
type v1Req struct {
	pid           string
	q             url.Values
	limit, offset int
	sortBy        string
}

func v1Parse(r *http.Request) (*v1Req, error) {
	q := r.URL.Query()
	raw := strings.TrimSpace(q.Get("project_id"))
	if raw == "" {
		return nil, store.Invalidf("project_id is required")
	}
	pid, err := project(raw)
	if err != nil {
		return nil, err
	}
	v := &v1Req{pid: pid, q: q, sortBy: q.Get("sort_by")}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"limit", &v.limit}, {"offset", &v.offset}} {
		if s := q.Get(p.name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				return nil, store.Invalidf("%s must be a non-negative integer", p.name)
			}
			*p.dst = n
		}
	}
	return v, nil
}

func boolParam(q url.Values, name string) bool {
	switch strings.ToLower(q.Get(name)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// v1 wraps a GET handler: parse common parameters, write JSON or the error.
func v1(fn func(ctx context.Context, r *http.Request, v *v1Req) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := v1Parse(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		out, err := fn(r.Context(), r, v)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
	}
}

func list[T any](items []T, total int) map[string]any {
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items, "total_count": total}
}

// sessionsQuery reads the /api/v1/sessions filters, each named prefix+name
// (the detections API takes them as session.<name>).
func sessionsQuery(pid string, q url.Values, prefix string) (store.SessionsQuery, bool, error) {
	get := func(name string) string { return strings.TrimSpace(q.Get(prefix + name)) }
	sq := store.SessionsQuery{ProjectID: pid, Agent: get("agent"), TeamID: get("team_id"),
		Status: get("status"), HasDetection: get("has_detection"), MinScore: get("min_score"), MaxScore: get("max_score"),
		ScoreBelow: get("score_below"), Model: get("model"), Tool: get("tool"), Q: get("q"), From: get("from"), To: get("to")}
	has := sq.Agent != "" || sq.TeamID != "" || sq.Status != "" || sq.HasDetection != "" || sq.MinScore != "" || sq.MaxScore != "" ||
		sq.ScoreBelow != "" || sq.Model != "" || sq.Tool != "" || sq.Q != ""
	if c := get("cost_min"); c != "" {
		f, err := strconv.ParseFloat(c, 64)
		if err != nil || f < 0 {
			return sq, false, store.Invalidf("%scost_min must be a non-negative number", prefix)
		}
		sq.CostMin = &f
		has = true
	}
	return sq, has, nil
}

func (s *Server) v1Sessions(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, _ *http.Request, v *v1Req) (any, error) {
		sq, _, err := sessionsQuery(v.pid, v.q, "")
		if err != nil {
			return nil, err
		}
		sq.Limit, sq.Offset, sq.SortBy = v.limit, v.offset, v.sortBy
		rows, total, err := s.store.QuerySessions(ctx, sq)
		if err != nil {
			return nil, err
		}
		return list(rows, total), nil
	})(w, r)
}

// v1SessionsAttention counts the attention cards over every matching session.
func (s *Server) v1SessionsAttention(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, _ *http.Request, v *v1Req) (any, error) {
		sq, _, err := sessionsQuery(v.pid, v.q, "")
		if err != nil {
			return nil, err
		}
		return s.store.SessionsAttention(ctx, sq)
	})(w, r)
}

func (s *Server) v1Session(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, r *http.Request, v *v1Req) (any, error) {
		return s.store.GetAgentSession(ctx, v.pid, pathParam(r, "id"))
	})(w, r)
}

func (s *Server) v1SessionTimeline(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, r *http.Request, v *v1Req) (any, error) {
		var types []string
		if t := v.q.Get("types"); t != "" {
			types = strings.Split(t, ",")
		}
		id := pathParam(r, "id")
		items, total, err := s.store.SessionTimeline(ctx, store.SessionTimelineQuery{ProjectID: v.pid, SessionID: id, Types: types,
			Agent: v.q.Get("agent"), Limit: v.limit, Offset: v.offset})
		if err != nil {
			return nil, err
		}
		out := list(items, total)
		out["session_id"] = id
		return out, nil
	})(w, r)
}

func (s *Server) v1SessionGraph(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, r *http.Request, v *v1Req) (any, error) {
		return s.store.SessionGraph(ctx, v.pid, pathParam(r, "id"))
	})(w, r)
}

func (s *Server) v1SessionReasoning(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, r *http.Request, v *v1Req) (any, error) {
		id := pathParam(r, "id")
		steps, total, err := s.store.SessionReasoning(ctx, store.SessionReasoningQuery{ProjectID: v.pid, SessionID: id, Agent: v.q.Get("agent"),
			Kind: v.q.Get("kind"), Q: v.q.Get("q"), IncludeDuplicates: boolParam(v.q, "include_duplicates"), Limit: v.limit, Offset: v.offset})
		if err != nil {
			return nil, err
		}
		out := list(steps, total)
		out["session_id"] = id
		return out, nil
	})(w, r)
}

func (s *Server) v1Agents(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, _ *http.Request, v *v1Req) (any, error) {
		rows, total, err := s.store.QueryAgentsV1(ctx, store.AgentsV1Query{ProjectID: v.pid, Q: v.q.Get("q"), TeamID: v.q.Get("team_id"),
			From: v.q.Get("from"), To: v.q.Get("to"), Limit: v.limit, Offset: v.offset, SortBy: v.sortBy})
		if err != nil {
			return nil, err
		}
		return list(rows, total), nil
	})(w, r)
}

func (s *Server) v1Agent(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, r *http.Request, v *v1Req) (any, error) {
		return s.store.GetAgentV1(ctx, v.pid, pathParam(r, "name"))
	})(w, r)
}

func (s *Server) v1Teams(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, _ *http.Request, v *v1Req) (any, error) {
		rows, total, err := s.store.QueryTeams(ctx, store.TeamsQuery{ProjectID: v.pid, Q: v.q.Get("q"), Agent: v.q.Get("agent"),
			From: v.q.Get("from"), To: v.q.Get("to"), Limit: v.limit, Offset: v.offset, SortBy: v.sortBy})
		if err != nil {
			return nil, err
		}
		return list(rows, total), nil
	})(w, r)
}

func (s *Server) v1Team(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, r *http.Request, v *v1Req) (any, error) {
		return s.store.GetAgentTeam(ctx, v.pid, pathParam(r, "id"))
	})(w, r)
}

func (s *Server) v1Handoffs(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, _ *http.Request, v *v1Req) (any, error) {
		q := v.q
		rows, total, err := s.store.QueryHandoffs(ctx, store.HandoffsQuery{ProjectID: v.pid, SessionID: q.Get("session_id"),
			FromAgent: q.Get("from_agent"), ToAgent: q.Get("to_agent"), Agent: q.Get("agent"), TeamID: q.Get("team_id"), Kind: q.Get("kind"),
			From: q.Get("from"), To: q.Get("to"), Limit: v.limit, Offset: v.offset, SortBy: v.sortBy})
		if err != nil {
			return nil, err
		}
		return list(rows, total), nil
	})(w, r)
}

func (s *Server) v1Scores(w http.ResponseWriter, r *http.Request) {
	v1(func(ctx context.Context, _ *http.Request, v *v1Req) (any, error) {
		q := v.q
		rows, total, err := s.store.QueryScopeScores(ctx, store.ScopeScoresQuery{ProjectID: v.pid, Scope: q.Get("scope"),
			SessionID: q.Get("session_id"), Agent: q.Get("agent"), HandoffID: q.Get("handoff_id"), Scorer: q.Get("scorer"),
			Limit: v.limit, Offset: v.offset})
		if err != nil {
			return nil, err
		}
		return list(rows, total), nil
	})(w, r)
}

func (s *Server) v1ScoresRun(ctx context.Context, r *http.Request, req *evals.ScopeRunReq) (any, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, store.Invalidf("project_id is required")
	}
	res, err := s.engine.RunScopeScorers(ctx, *req, userFrom(r, nil))
	if err != nil {
		return nil, err
	}
	return list(res, len(res)), nil
}
