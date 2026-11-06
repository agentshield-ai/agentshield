package server

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/agentshield-ai/agentshield/internal/weft/automations"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Automations: Automation objects are stored with /obj/create; these
// endpoints test-fire them, force an error-rate check and list runs.

var (
	runnersMu sync.Mutex
	runners   = map[*Server]*automations.Runner{}
)

func (s *Server) automations() *automations.Runner {
	runnersMu.Lock()
	defer runnersMu.Unlock()
	return runners[s]
}

func init() {
	startExtensions = append(startExtensions, func(s *Server) func() {
		ar := automations.New(s.store, s.llm.LookupURLSecret, s.log)
		ar.Start()
		runnersMu.Lock()
		runners[s] = ar
		runnersMu.Unlock()
		return func() {
			ar.Close()
			runnersMu.Lock()
			delete(runners, s)
			runnersMu.Unlock()
		}
	})
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/automations/runs/query", handle(s.automationRunsQuery))
		r.Post("/automations/test", handle(s.automationTest))
		r.Post("/automations/check", handle(s.automationCheck))
		r.Post("/automations/validate", handle(s.automationValidate))
	})
}

type automationRunsReq struct {
	ProjectID    string `json:"project_id"`
	AutomationID string `json:"automation_id"`
	Limit        int    `json:"limit"`
	Offset       int    `json:"offset"`
}

func (s *Server) automationRunsQuery(ctx context.Context, _ *http.Request, req *automationRunsReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	runs, err := s.store.QueryAutomationRuns(ctx, pid, req.AutomationID, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"runs": runs}, nil
}

type automationTestReq struct {
	ProjectID    string `json:"project_id"`
	AutomationID string `json:"automation_id"`
	Digest       string `json:"digest"`
	// Val test-fires an unsaved automation value.
	Val   any            `json:"val"`
	Event map[string]any `json:"event"`
}

func (s *Server) automationTest(ctx context.Context, _ *http.Request, req *automationTestReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	var obj *store.Obj
	switch {
	case req.Val != nil:
		name := req.AutomationID
		if m, ok := req.Val.(map[string]any); ok && name == "" {
			name, _ = m["name"].(string)
		}
		if strings.TrimSpace(name) == "" {
			name = "unsaved-automation"
		}
		obj = &store.Obj{ProjectID: pid, ObjectID: store.SanitizeObjectID(name), Val: req.Val}
	case req.AutomationID != "":
		d := req.Digest
		if d == "" {
			d = "latest"
		}
		obj, err = s.store.ReadObject(ctx, pid, req.AutomationID, d)
		if err != nil {
			return nil, err
		}
	default:
		return nil, store.Invalidf("automation_id or val is required")
	}
	a, err := automations.Parse(obj.Val)
	if err != nil {
		return nil, err
	}
	event := req.Event
	if event == nil {
		event = automations.SampleEvent(a)
	}
	run, err := s.automations().Fire(ctx, obj, a, event, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": run}, nil
}

type automationCheckReq struct {
	ProjectID string `json:"project_id"`
}

// automationCheck evaluates error_rate triggers now instead of waiting for
// the background loop.
func (s *Server) automationCheck(ctx context.Context, _ *http.Request, req *automationCheckReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	ar := s.automations()
	ar.Invalidate(pid)
	runs, err := ar.CheckErrorRates(ctx, pid)
	if err != nil {
		return nil, err
	}
	if runs == nil {
		runs = []*store.AutomationRun{}
	}
	return map[string]any{"runs": runs}, nil
}

type automationValidateReq struct {
	Val any `json:"val"`
}

func (s *Server) automationValidate(_ context.Context, _ *http.Request, req *automationValidateReq) (any, error) {
	if _, err := automations.Parse(req.Val); err != nil {
		return nil, err
	}
	return map[string]any{"valid": true}, nil
}
