package server

import (
	"context"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Feedback is a reaction, note, annotation or score attached to a ref. Its
// span_ref is the scorer's own span, for scorer results.
type Feedback struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	Ref           string  `json:"ref"`
	FeedbackType  string  `json:"feedback_type"`
	Payload       any     `json:"payload"`
	Creator       *string `json:"creator"`
	CreatedAt     string  `json:"created_at"`
	UserID        *string `json:"user_id"`
	AnnotationRef *string `json:"annotation_ref"`
	RunnableRef   *string `json:"runnable_ref"`
	SpanRef       *string `json:"span_ref"`
	TriggerRef    *string `json:"trigger_ref"`
	QueueID       *string `json:"queue_id"`
}

func feedbackFrom(f store.Feedback) Feedback {
	return Feedback{ID: f.ID, ProjectID: f.ProjectID, Ref: f.Ref, FeedbackType: f.FeedbackType, Payload: f.Payload,
		Creator: f.Creator, CreatedAt: f.CreatedAt, UserID: f.UserID, AnnotationRef: f.AnnotationRef,
		RunnableRef: f.RunnableRef, SpanRef: f.CallRef, TriggerRef: f.TriggerRef, QueueID: f.QueueID}
}

// feedbackCreateReq is the body of /api/v1/feedback/create (and an item of
// /api/v1/feedback/batch, which also takes the scorer and span fields of
// the agent feedback schema; docs/weft/API.md "Agents").
type feedbackCreateReq struct {
	store.AgentFeedbackItem
	SpanRef *string `json:"span_ref"`
}

func (f *feedbackCreateReq) item() store.AgentFeedbackItem {
	it := f.AgentFeedbackItem
	if f.SpanRef != nil {
		it.CallRef = f.SpanRef
	}
	return it
}

func feedbackCreated(f *store.Feedback) map[string]any {
	return map[string]any{"id": f.ID, "created_at": f.CreatedAt, "user_id": f.UserID, "payload": f.Payload}
}

func (s *Server) v1FeedbackCreate(ctx context.Context, r *http.Request, req *feedbackCreateReq) (any, error) {
	it := req.item()
	it.UserID = userFrom(r, it.UserID)
	f, err := s.store.CreateAgentFeedback(ctx, it)
	if err != nil {
		return nil, err
	}
	return feedbackCreated(f), nil
}

type feedbackBatchNativeReq struct {
	ProjectID string              `json:"project_id"`
	Items     []feedbackCreateReq `json:"items"`
}

func (s *Server) v1FeedbackBatch(ctx context.Context, r *http.Request, req *feedbackBatchNativeReq) (any, error) {
	if len(req.Items) > 1000 {
		return nil, store.Invalidf("batch too large (max 1000)")
	}
	res := make([]any, 0, len(req.Items))
	for _, x := range req.Items {
		it := x.item()
		if it.ProjectID == "" {
			it.ProjectID = req.ProjectID
		}
		it.UserID = userFrom(r, it.UserID)
		f, err := s.store.CreateAgentFeedback(ctx, it)
		if err != nil {
			return nil, err
		}
		res = append(res, feedbackCreated(f))
	}
	return map[string]any{"items": res}, nil
}

type feedbackQueryNativeReq struct {
	ProjectID string         `json:"project_id"`
	Query     any            `json:"query"`
	SortBy    []store.SortBy `json:"sort_by"`
	Limit     *int           `json:"limit"`
	Offset    int            `json:"offset"`
}

func (s *Server) v1FeedbackQuery(ctx context.Context, _ *http.Request, req *feedbackQueryNativeReq) (any, error) {
	limit, err := listLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	total, err := s.store.CountFeedback(ctx, req.ProjectID, req.Query)
	if err != nil {
		return nil, err
	}
	items := []Feedback{}
	if limit > 0 {
		fs, err := s.store.QueryFeedback(ctx, req.ProjectID, req.Query, req.SortBy, limit, req.Offset)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			items = append(items, feedbackFrom(f))
		}
	}
	return newList(items, total), nil
}

type feedbackDeleteReq struct {
	ProjectID string   `json:"project_id"`
	IDs       []string `json:"ids"`
	Query     any      `json:"query"`
}

// v1FeedbackDelete deletes feedback by id, or every feedback matching a query.
func (s *Server) v1FeedbackDelete(ctx context.Context, _ *http.Request, req *feedbackDeleteReq) (any, error) {
	q := req.Query
	switch {
	case len(req.IDs) > 0 && q != nil:
		return nil, store.Invalidf("send ids or query, not both")
	case len(req.IDs) > 0:
		ids := make([]any, len(req.IDs))
		for i, id := range req.IDs {
			ids[i] = map[string]any{"$literal": id}
		}
		q = map[string]any{"$expr": map[string]any{"$in": []any{map[string]any{"$getField": "id"}, ids}}}
	case q == nil:
		return nil, store.Invalidf("ids or query is required")
	}
	return empty, s.store.PurgeFeedback(ctx, req.ProjectID, q)
}

type feedbackReplaceNativeReq struct {
	feedbackCreateReq
	FeedbackID string `json:"feedback_id"`
}

func (s *Server) v1FeedbackReplace(ctx context.Context, r *http.Request, req *feedbackReplaceNativeReq) (any, error) {
	it := req.item()
	it.UserID = userFrom(r, it.UserID)
	f, err := s.store.ReplaceFeedback(ctx, req.FeedbackID, store.FeedbackCreate{ProjectID: it.ProjectID, Ref: it.Ref,
		FeedbackType: it.FeedbackType, Payload: it.Payload, Creator: it.Creator, UserID: it.UserID, AnnotationRef: it.AnnotationRef,
		RunnableRef: it.RunnableRef, CallRef: it.CallRef, TriggerRef: it.TriggerRef, QueueID: it.QueueID})
	if err != nil {
		return nil, err
	}
	return feedbackCreated(f), nil
}

// ---------------------------------------------------------------- prices

type pricesCreateReq struct {
	ProjectID string                     `json:"project_id"`
	Prices    map[string]store.CostInput `json:"prices"`
}

func (s *Server) v1PricesCreate(ctx context.Context, _ *http.Request, req *pricesCreateReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.Prices) == 0 {
		return nil, store.Invalidf("prices is required")
	}
	ids, err := s.store.CreateCosts(ctx, pid, req.Prices)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(ids))
	for i, x := range ids {
		out[i] = map[string]string{"id": x[0], "llm_id": x[1]}
	}
	return map[string]any{"items": out}, nil
}

type pricesQueryReq struct {
	ProjectID       string `json:"project_id"`
	Query           any    `json:"query"`
	Limit           *int   `json:"limit"`
	Offset          int    `json:"offset"`
	IncludeDefaults bool   `json:"include_defaults"`
}

func (s *Server) v1PricesQuery(ctx context.Context, _ *http.Request, req *pricesQueryReq) (any, error) {
	limit, err := listLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	all, err := s.store.QueryCosts(ctx, pid, req.Query, 0, 0)
	if err != nil {
		return nil, err
	}
	if req.IncludeDefaults {
		all = append(all, store.DefaultPriceList()...)
	}
	total := int64(len(all))
	if req.Offset > len(all) {
		all = nil
	} else {
		all = all[req.Offset:]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return newList(all, total), nil
}

type pricesDeleteReq struct {
	ProjectID string   `json:"project_id"`
	IDs       []string `json:"ids"`
	Query     any      `json:"query"`
}

func (s *Server) v1PricesDelete(ctx context.Context, _ *http.Request, req *pricesDeleteReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.IDs) == 0 && req.Query == nil {
		return nil, store.Invalidf("ids or query is required")
	}
	return empty, s.store.PurgeCosts(ctx, pid, req.IDs, req.Query)
}
