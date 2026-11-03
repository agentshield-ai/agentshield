package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Annotation queues (review queues in the UI) under /annotation_queues.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/annotation_queues", handle(s.queueCreate))
		r.Post("/annotation_queues/query", s.queueQuery)
		r.Post("/annotation_queues/stats", handle(s.queueStats))
		r.Get("/annotation_queues/{queue_id}", handle(s.queueRead))
		r.Put("/annotation_queues/{queue_id}", handle(s.queueUpdate))
		r.Delete("/annotation_queues/{queue_id}", handle(s.queueDelete))
		r.Post("/annotation_queues/{queue_id}/items", handle(s.queueItemsAdd))
		r.Post("/annotation_queues/{queue_id}/items/query", handle(s.queueItemsQuery))
		r.Post("/annotation_queues/{queue_id}/items/{item_id}/progress", handle(s.queueItemProgress))
		r.Delete("/annotation_queues/{queue_id}/items/{item_id}", handle(s.queueItemDelete))
	})
}

// queueProject reads project_id from the body or, for GET/DELETE, the URL.
func queueProject(r *http.Request, body string) (string, error) {
	if body == "" {
		body = r.URL.Query().Get("project_id")
	}
	if strings.TrimSpace(body) == "" {
		return "", store.Invalidf("project_id is required")
	}
	return project(body)
}

type queueCreateReq struct {
	ProjectID   string   `json:"project_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ScorerRefs  []string `json:"scorer_refs"`
	UserID      *string  `json:"user_id"`
}

func (s *Server) queueCreate(ctx context.Context, r *http.Request, req *queueCreateReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	q, err := s.store.CreateAnnotationQueue(ctx, pid, req.Name, req.Description, req.ScorerRefs, userFrom(r, req.UserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": q.ID}, nil
}

type queueQueryReq struct {
	ProjectID string            `json:"project_id"`
	Name      string            `json:"name"`
	Limit     int               `json:"limit"`
	Offset    int               `json:"offset"`
	SortBy    []store.QueueSort `json:"sort_by"`
}

// queueQuery streams annotation queues as NDJSON.
func (s *Server) queueQuery(w http.ResponseWriter, r *http.Request) {
	var req queueQueryReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	qs, err := s.store.QueryAnnotationQueues(r.Context(), pid, req.Name, req.SortBy, req.Limit, req.Offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, q := range qs {
		if err := enc.Encode(q); err != nil {
			return
		}
	}
}

type queueStatsReq struct {
	ProjectID string   `json:"project_id"`
	QueueIDs  []string `json:"queue_ids"`
}

func (s *Server) queueStats(ctx context.Context, r *http.Request, req *queueStatsReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.QueueIDs) > 1000 {
		return nil, store.Invalidf("at most 1000 queue_ids")
	}
	st, err := s.store.AnnotationQueueStats(ctx, pid, req.QueueIDs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stats": st}, nil
}

type queueIDReq struct {
	ProjectID string `json:"project_id"`
}

func (s *Server) queueRead(ctx context.Context, r *http.Request, req *queueIDReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	q, err := s.store.GetAnnotationQueue(ctx, pid, chi.URLParam(r, "queue_id"), false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"queue": q}, nil
}

type queueUpdateReq struct {
	ProjectID   string    `json:"project_id"`
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	ScorerRefs  *[]string `json:"scorer_refs"`
}

func (s *Server) queueUpdate(ctx context.Context, r *http.Request, req *queueUpdateReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	var refsList []string
	if req.ScorerRefs != nil {
		refsList = *req.ScorerRefs
	}
	q, err := s.store.UpdateAnnotationQueue(ctx, pid, chi.URLParam(r, "queue_id"), req.Name, req.Description, refsList, req.ScorerRefs != nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"queue": q}, nil
}

func (s *Server) queueDelete(ctx context.Context, r *http.Request, req *queueIDReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	q, err := s.store.DeleteAnnotationQueue(ctx, pid, chi.URLParam(r, "queue_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"queue": q}, nil
}

type queueItemsAddReq struct {
	ProjectID     string   `json:"project_id"`
	CallIDs       []string `json:"call_ids"`
	DisplayFields []string `json:"display_fields"`
	UserID        *string  `json:"user_id"`
}

func (s *Server) queueItemsAdd(ctx context.Context, r *http.Request, req *queueItemsAddReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	added, dups, err := s.store.AddAnnotationQueueItems(ctx, pid, chi.URLParam(r, "queue_id"), req.CallIDs, req.DisplayFields, userFrom(r, req.UserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"added_count": added, "duplicates": dups}, nil
}

type queueItemsQueryReq struct {
	ProjectID       string                  `json:"project_id"`
	Filter          *store.QueueItemsFilter `json:"filter"`
	SortBy          []store.QueueSort       `json:"sort_by"`
	Limit           int                     `json:"limit"`
	Offset          int                     `json:"offset"`
	IncludePosition bool                    `json:"include_position"`
	// AnnotatorUserID shows another annotator's progress (Weft extension).
	AnnotatorUserID *string `json:"annotator_user_id"`
}

func (s *Server) queueItemsQuery(ctx context.Context, r *http.Request, req *queueItemsQueryReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	items, err := s.store.QueryAnnotationQueueItems(ctx, pid, chi.URLParam(r, "queue_id"), req.Filter, req.SortBy, req.Limit, req.Offset,
		req.IncludePosition, userFrom(r, req.AnnotatorUserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

type queueProgressReq struct {
	ProjectID       string `json:"project_id"`
	AnnotationState string `json:"annotation_state"`
}

func (s *Server) queueItemProgress(ctx context.Context, r *http.Request, req *queueProgressReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	it, err := s.store.SetAnnotationQueueItemState(ctx, pid, chi.URLParam(r, "queue_id"), chi.URLParam(r, "item_id"), req.AnnotationState, userFrom(r, nil))
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": it}, nil
}

type queueItemDeleteReq struct {
	ProjectID string `json:"project_id"`
}

// queueItemDelete removes one item from a queue (the UI offers undo by
// delaying this call).
func (s *Server) queueItemDelete(ctx context.Context, r *http.Request, req *queueItemDeleteReq) (any, error) {
	pid, err := queueProject(r, req.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteAnnotationQueueItem(ctx, pid, chi.URLParam(r, "queue_id"), chi.URLParam(r, "item_id")); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}
