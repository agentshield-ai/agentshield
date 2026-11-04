package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Object is one version of a versioned value (docs/weft/DESIGN.md §10.2).
// kind is "function", the kind of the value's base class (dataset, model,
// prompt, scorer, evaluation, leaderboard, monitor, annotation_spec,
// saved_view, provider, provider_model, dashboard, automation) or
// "object"; class is the most specific known class (StringPrompt,
// LLMAsAJudgeScorer, ...) or the class the publisher forced, null for plain
// values.
type Object struct {
	ProjectID    string          `json:"project_id"`
	Name         string          `json:"name"`
	Digest       string          `json:"digest"`
	Version      int             `json:"version"`
	Kind         string          `json:"kind"`
	Class        *string         `json:"class"`
	Value        json.RawMessage `json:"value,omitempty"`
	Ref          string          `json:"ref"`
	IsLatest     bool            `json:"is_latest"`
	Aliases      []string        `json:"aliases"`
	Tags         []string        `json:"tags"`
	UserID       *string         `json:"user_id"`
	CreatedAt    string          `json:"created_at"`
	DeletedAt    *string         `json:"deleted_at"`
	SizeBytes    int64           `json:"size_bytes"`
	VersionCount *int            `json:"version_count,omitempty"`
}

func objectFrom(o *store.Obj, withValue bool) (*Object, error) {
	out := &Object{ProjectID: o.ProjectID, Name: o.ObjectID, Digest: o.Digest, Version: o.VersionIndex, Kind: store.ObjectKind(o),
		Class: o.LeafObjectClass, Ref: o.Ref(), IsLatest: o.IsLatest, Aliases: o.Aliases, Tags: o.Tags, UserID: o.UserID,
		CreatedAt: o.CreatedAt, DeletedAt: o.DeletedAt, SizeBytes: o.SizeBytes, VersionCount: o.VersionCount}
	if out.Kind == "function" {
		out.Class = nil
	}
	if out.Aliases == nil {
		out.Aliases = []string{}
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if withValue {
		// The stored value with its publisher's key order (Obj.MarshalJSON).
		raw, err := json.Marshal(o)
		if err != nil {
			return nil, err
		}
		var v struct {
			Val json.RawMessage `json:"val"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out.Value = v.Val
	}
	return out, nil
}

type objectSaveReq struct {
	ProjectID string          `json:"project_id"`
	Name      string          `json:"name"`
	Value     json.RawMessage `json:"value"`
	Class     string          `json:"class"`
	UserID    *string         `json:"user_id"`
}

func (s *Server) v1ObjectSave(ctx context.Context, r *http.Request, req *objectSaveReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.Value) == 0 || string(req.Value) == "null" {
		return nil, store.Invalidf("value is required")
	}
	d, v, err := s.store.CreateObjectRaw(ctx, pid, req.Name, nil, req.Value, req.Class, userFrom(r, req.UserID))
	if err != nil {
		return nil, err
	}
	s.engine.InvalidateMonitors(pid)
	o, err := s.store.ReadObject(ctx, pid, req.Name, d)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": req.Name, "digest": d, "version": v, "kind": store.ObjectKind(o), "ref": o.Ref()}, nil
}

// v1ObjectGet reads the latest version, or the version named by digest,
// "v<N>" or an alias.
func (s *Server) v1ObjectGet(w http.ResponseWriter, r *http.Request) {
	version := pathParam(r, "version")
	if version == "" {
		version = "latest"
	}
	o, err := s.store.ReadObject(r.Context(), r.URL.Query().Get("project_id"), pathParam(r, "name"), version)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := objectFrom(o, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

type objectsFilter struct {
	Names      []string `json:"names"`
	Kinds      []string `json:"kinds"`
	Classes    []string `json:"classes"`
	LatestOnly bool     `json:"latest_only"`
}

// UnmarshalJSON rejects unknown filter fields.
func (f *objectsFilter) UnmarshalJSON(b []byte) error {
	type plain objectsFilter
	var p plain
	if err := strictJSON(b, &p); err != nil {
		return err
	}
	*f = objectsFilter(p)
	return nil
}

type objectsQueryReq struct {
	ProjectID      string         `json:"project_id"`
	Filter         *objectsFilter `json:"filter"`
	Limit          *int           `json:"limit"`
	Offset         int            `json:"offset"`
	SortBy         []store.SortBy `json:"sort_by"`
	MetadataOnly   bool           `json:"metadata_only"`
	IncludeDeleted bool           `json:"include_deleted"`
}

// objectSortFields maps Weft's object sort fields to the store's.
var objectSortFields = map[string]string{"created_at": "created_at", "name": "object_id", "version": "version_index",
	"size_bytes": "size_bytes"}

func (s *Server) v1ObjectsQuery(ctx context.Context, _ *http.Request, req *objectsQueryReq) (any, error) {
	limit, err := listLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	q := store.ObjsQuery{ProjectID: req.ProjectID, Offset: req.Offset, MetadataOnly: req.MetadataOnly, IncludeDeleted: req.IncludeDeleted,
		Filter: &store.ObjsFilter{}}
	if f := req.Filter; f != nil {
		q.Filter = &store.ObjsFilter{ObjectIDs: f.Names, Kinds: f.Kinds, LeafObjectClasses: f.Classes, LatestOnly: f.LatestOnly}
	}
	for _, sb := range req.SortBy {
		f, ok := objectSortFields[sb.Field]
		if !ok {
			return nil, store.Invalidf("cannot sort objects by %q (created_at, name, version, size_bytes)", sb.Field)
		}
		q.SortBy = append(q.SortBy, store.SortBy{Field: f, Direction: sb.Direction})
	}
	total, err := s.store.CountObjects(ctx, q)
	if err != nil {
		return nil, err
	}
	items := []*Object{}
	if limit > 0 {
		q.Limit = limit
		objs, err := s.store.QueryObjects(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			x, err := objectFrom(o, !req.MetadataOnly)
			if err != nil {
				return nil, err
			}
			items = append(items, x)
		}
	}
	return newList(items, total), nil
}

type objectsDeleteReq struct {
	ProjectID string   `json:"project_id"`
	Name      string   `json:"name"`
	Digests   []string `json:"digests"`
}

func (s *Server) v1ObjectsDelete(ctx context.Context, _ *http.Request, req *objectsDeleteReq) (any, error) {
	n, err := s.store.DeleteObject(ctx, req.ProjectID, req.Name, req.Digests)
	if err != nil {
		return nil, err
	}
	pid, _ := project(req.ProjectID)
	s.engine.InvalidateMonitors(pid)
	return map[string]any{"num_deleted": n}, nil
}

type projectOnlyReq struct {
	ProjectID string `json:"project_id"`
}

func (s *Server) v1ObjectTagsList(ctx context.Context, _ *http.Request, req *projectOnlyReq) (any, error) {
	tags, err := s.store.ListTags(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	return newList(tags, int64(len(tags))), nil
}

func (s *Server) v1ObjectAliasesList(ctx context.Context, _ *http.Request, req *projectOnlyReq) (any, error) {
	al, err := s.store.ListAliases(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	return newList(al, int64(len(al))), nil
}

type objectAliasesReq struct {
	ProjectID string   `json:"project_id"`
	Version   string   `json:"version"`
	Aliases   []string `json:"aliases"`
}

// v1ObjectAliasesSet points each alias at the version (a digest, "v<N>",
// "latest" or another alias).
func (s *Server) v1ObjectAliasesSet(ctx context.Context, r *http.Request, req *objectAliasesReq) (any, error) {
	if req.Version == "" {
		return nil, store.Invalidf("version is required")
	}
	if len(req.Aliases) == 0 || len(req.Aliases) > 100 {
		return nil, store.Invalidf("aliases must list 1 to 100 aliases")
	}
	for _, a := range req.Aliases {
		if err := s.store.SetAlias(ctx, req.ProjectID, pathParam(r, "name"), req.Version, a); err != nil {
			return nil, err
		}
	}
	return empty, nil
}

func (s *Server) v1ObjectAliasesRemove(ctx context.Context, r *http.Request, req *objectAliasesReq) (any, error) {
	for _, a := range req.Aliases {
		if err := s.store.RemoveAlias(ctx, req.ProjectID, pathParam(r, "name"), a); err != nil {
			return nil, err
		}
	}
	return empty, nil
}

type objectTagsReq struct {
	ProjectID string   `json:"project_id"`
	Tags      []string `json:"tags"`
}

func (s *Server) v1ObjectTagsAdd(ctx context.Context, r *http.Request, req *objectTagsReq) (any, error) {
	return empty, s.store.AddTags(ctx, req.ProjectID, pathParam(r, "name"), pathParam(r, "version"), req.Tags)
}

func (s *Server) v1ObjectTagsRemove(ctx context.Context, r *http.Request, req *objectTagsReq) (any, error) {
	return empty, s.store.RemoveTags(ctx, req.ProjectID, pathParam(r, "name"), pathParam(r, "version"), req.Tags)
}

// ---------------------------------------------------------------- tables

type tableCreateNativeReq struct {
	ProjectID  string            `json:"project_id"`
	Rows       []json.RawMessage `json:"rows"`
	RowDigests []string          `json:"row_digests"`
}

// v1TableCreate stores rows, or builds a table from rows already stored
// (row_digests).
func (s *Server) v1TableCreate(ctx context.Context, _ *http.Request, req *tableCreateNativeReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	var d string
	var rows []string
	switch {
	case req.Rows != nil && req.RowDigests != nil:
		return nil, store.Invalidf("send rows or row_digests, not both")
	case req.RowDigests != nil:
		rows = req.RowDigests
		d, err = s.store.CreateTableFromDigests(ctx, pid, rows)
	default:
		d, rows, err = s.store.CreateTableRaw(ctx, pid, req.Rows)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"digest": d, "row_digests": rows, "ref": refs.TableRef(pid, d)}, nil
}

type tableUpdateNativeReq struct {
	ProjectID string              `json:"project_id"`
	Digest    string              `json:"digest"`
	Updates   []store.TableUpdate `json:"updates"`
}

func (s *Server) v1TableUpdate(ctx context.Context, _ *http.Request, req *tableUpdateNativeReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	d, rows, err := s.store.UpdateTable(ctx, pid, req.Digest, req.Updates)
	if err != nil {
		return nil, err
	}
	return map[string]any{"digest": d, "updated_row_digests": rows, "ref": refs.TableRef(pid, d)}, nil
}

// TableRow is one row of a table.
type TableRow struct {
	Digest string          `json:"digest"`
	Index  int             `json:"index"`
	Value  json.RawMessage `json:"value"`
}

func tableRowFrom(r store.TableRow) (TableRow, error) {
	raw, err := json.Marshal(r) // the stored text, publisher's key order
	if err != nil {
		return TableRow{}, err
	}
	var v struct {
		Val json.RawMessage `json:"val"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return TableRow{}, err
	}
	return TableRow{Digest: r.Digest, Index: r.OriginalIndex, Value: v.Val}, nil
}

type tableQueryNativeReq struct {
	ProjectID string `json:"project_id"`
	Digest    string `json:"digest"`
	Filter    *struct {
		RowDigests []string `json:"row_digests"`
	} `json:"filter"`
	Limit  *int           `json:"limit"`
	Offset int            `json:"offset"`
	SortBy []store.SortBy `json:"sort_by"`
}

func (q *tableQueryNativeReq) store(limit int) store.TableQuery {
	tq := store.TableQuery{ProjectID: q.ProjectID, Digest: q.Digest, Limit: limit, Offset: q.Offset}
	if q.Filter != nil {
		tq.Filter = &struct {
			RowDigests []string `json:"row_digests"`
		}{q.Filter.RowDigests}
	}
	for _, sb := range q.SortBy {
		// Rows sort by a path into the value: "value.x" (or "x").
		if f, ok := cutPrefix(sb.Field, "value."); ok {
			sb.Field = "val." + f
		}
		tq.SortBy = append(tq.SortBy, sb)
	}
	return tq
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return s, false
}

func (s *Server) v1TableQuery(ctx context.Context, _ *http.Request, req *tableQueryNativeReq) (any, error) {
	limit, err := listLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	total, err := s.store.TableCount(ctx, pid, req.Digest)
	if err != nil {
		return nil, err
	}
	if req.Filter != nil && len(req.Filter.RowDigests) > 0 {
		all, err := s.store.QueryTable(ctx, req.store(0))
		if err != nil {
			return nil, err
		}
		total = len(all)
	}
	items := []TableRow{}
	if limit > 0 {
		rows, err := s.store.QueryTable(ctx, req.store(limit))
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			x, err := tableRowFrom(r)
			if err != nil {
				return nil, err
			}
			items = append(items, x)
		}
	}
	return newList(items, int64(total)), nil
}

func (s *Server) v1TableStream(w http.ResponseWriter, r *http.Request) {
	var req tableQueryNativeReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	limit := 0 // all rows (the store caps a read at 100,000)
	if req.Limit != nil {
		if *req.Limit < 0 {
			writeErr(w, store.Invalidf("limit must not be negative"))
			return
		}
		limit = *req.Limit
	}
	rows, err := s.store.QueryTable(r.Context(), req.store(limit))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := newNDJSON(w)
	for _, row := range rows {
		x, err := tableRowFrom(row)
		if err != nil || out.write(x) != nil {
			return
		}
	}
	out.flush()
}

type tableStatsNativeReq struct {
	ProjectID          string   `json:"project_id"`
	Digest             string   `json:"digest"`
	Digests            []string `json:"digests"`
	IncludeStorageSize bool     `json:"include_storage_size"`
}

func (s *Server) v1TableStats(ctx context.Context, _ *http.Request, req *tableStatsNativeReq) (any, error) {
	digests := req.Digests
	if req.Digest != "" {
		digests = append([]string{req.Digest}, digests...)
	}
	if len(digests) == 0 || len(digests) > 1000 {
		return nil, store.Invalidf("digest or digests (1 to 1000) is required")
	}
	stats, err := s.store.TableStatsBatch(ctx, req.ProjectID, digests, req.IncludeStorageSize)
	if err != nil {
		return nil, err
	}
	if req.Digest != "" && len(req.Digests) == 0 && len(stats) == 0 {
		return nil, store.ErrNotFound
	}
	return newList(stats, int64(len(stats))), nil
}

// ---------------------------------------------------------------- refs and files

type refsReadReq struct {
	Refs []string `json:"refs"`
}

func (s *Server) v1RefsRead(ctx context.Context, _ *http.Request, req *refsReadReq) (any, error) {
	if len(req.Refs) > 1000 {
		return nil, store.Invalidf("too many refs (max 1000)")
	}
	// Stored JSON as published (key order, number text); spans in the Span
	// schema.
	vals, err := s.store.ReadRefsRaw(nativeRefs(ctx), req.Refs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": vals}, nil
}

func (s *Server) v1FileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 65<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart form with project_id and file")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file")
		return
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading file")
		return
	}
	pid, err := project(r.FormValue("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.store.CreateFile(r.Context(), pid, b)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"digest": d, "size_bytes": len(b)})
}

// v1FileGet serves a file's bytes with its sniffed content type, sandboxed
// (an <img src> can use it, with the api_key query parameter when needed).
func (s *Server) v1FileGet(w http.ResponseWriter, r *http.Request) {
	b, ctype, err := s.store.FileContent(r.Context(), r.URL.Query().Get("project_id"), pathParam(r, "digest"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(b)
}
