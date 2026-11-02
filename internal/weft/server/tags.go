package server

import (
	"context"
	"net/http"
	"net/url"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Weave's object tag and alias REST endpoints. Aliases share storage with
// /obj/alias/set|remove.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/tags", s.tagsList)
		r.Get("/aliases", s.aliasesList)
		r.Put("/objs/{object_id}/aliases", handle(s.objAliasesSet))
		r.Post("/objs/{object_id}/aliases", handle(s.objAliasesSet))
		r.Post("/objs/{object_id}/aliases/remove", handle(s.objAliasesRemove))
		r.Put("/objs/{object_id}/versions/{digest}/tags", handle(s.objTagsAdd))
		r.Post("/objs/{object_id}/versions/{digest}/tags", handle(s.objTagsAdd))
		r.Post("/objs/{object_id}/versions/{digest}/tags/remove", handle(s.objTagsRemove))
	})
}

func pathParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

func (s *Server) tagsList(w http.ResponseWriter, r *http.Request) {
	tags, err := s.store.ListTags(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"tags": tags})
}

func (s *Server) aliasesList(w http.ResponseWriter, r *http.Request) {
	al, err := s.store.ListAliases(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"aliases": al})
}

type objAliasesReq struct {
	ProjectID string   `json:"project_id"`
	Digest    string   `json:"digest"`
	Aliases   []string `json:"aliases"`
}

func (s *Server) objAliasesSet(ctx context.Context, r *http.Request, req *objAliasesReq) (any, error) {
	if req.Digest == "" {
		return nil, store.Invalidf("digest is required")
	}
	if len(req.Aliases) > 100 {
		return nil, store.Invalidf("too many aliases (max 100)")
	}
	oid := pathParam(r, "object_id")
	for _, a := range req.Aliases {
		if err := s.store.SetAlias(ctx, req.ProjectID, oid, req.Digest, a); err != nil {
			return nil, err
		}
	}
	return empty, nil
}

func (s *Server) objAliasesRemove(ctx context.Context, r *http.Request, req *objAliasesReq) (any, error) {
	oid := pathParam(r, "object_id")
	for _, a := range req.Aliases {
		if err := s.store.RemoveAlias(ctx, req.ProjectID, oid, a); err != nil {
			return nil, err
		}
	}
	return empty, nil
}

type objTagsReq struct {
	ProjectID string   `json:"project_id"`
	Tags      []string `json:"tags"`
}

func (s *Server) objTagsAdd(ctx context.Context, r *http.Request, req *objTagsReq) (any, error) {
	return empty, s.store.AddTags(ctx, req.ProjectID, pathParam(r, "object_id"), pathParam(r, "digest"), req.Tags)
}

func (s *Server) objTagsRemove(ctx context.Context, r *http.Request, req *objTagsReq) (any, error) {
	return empty, s.store.RemoveTags(ctx, req.ProjectID, pathParam(r, "object_id"), pathParam(r, "digest"), req.Tags)
}
