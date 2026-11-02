package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// POST /files/resolve_url turns file references into download URLs, and
// POST /image/create generates images (playground image generation).

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/files/resolve_url", handle(s.filesResolveURL))
		r.Post("/image/create", handle(s.imageCreate))
	})
}

const (
	defaultPresignTTL = time.Hour
	maxPresignTTL     = 7 * 24 * time.Hour
	maxResolveURIs    = 1000
)

type filesResolveReq struct {
	ProjectID string   `json:"project_id"`
	URIs      []string `json:"uris"`
	// ExpiresIn (seconds) is a Weft extension for presigned URL lifetime.
	ExpiresIn *int64 `json:"expires_in"`
}

var fileDigestRe = regexp.MustCompile(`^[A-Za-z0-9]{20,64}$`)

// filesResolveURL resolves each uri to a download URL:
//   - a file digest (as returned by /file/create; "weft-file://<digest>" and
//     "weave-file://<digest>" are accepted too): a presigned URL when the file
//     lives in a presigning backend (S3), otherwise this server's /file/content URL;
//   - s3://<backend bucket>/<key> under this project's prefix: a presigned URL;
//   - s3:// and gs:// URIs in a reference bucket registered for the project
//     or its team (refbuckets.go): a presigned (S3) or V4 signed (GCS) URL;
//   - http(s) URLs: returned unchanged.
//
// expires_at is the earliest expiry of the presigned URLs, or null.
func (s *Server) filesResolveURL(ctx context.Context, r *http.Request, req *filesResolveReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.URIs) > maxResolveURIs {
		return nil, store.Invalidf("too many uris (max %d)", maxResolveURIs)
	}
	ttl := defaultPresignTTL
	if req.ExpiresIn != nil {
		ttl = time.Duration(*req.ExpiresIn) * time.Second
		if ttl < time.Second || ttl > maxPresignTTL {
			return nil, store.Invalidf("expires_in must be between 1 and %d seconds", int64(maxPresignTTL/time.Second))
		}
	}
	backend := s.store.FileBackend()
	presigner, _ := backend.(store.FilePresigner)
	var earliest *time.Time
	presign := func(key string) (string, error) {
		u, exp, err := presigner.PresignGet(key, ttl)
		if err != nil {
			return "", err
		}
		if earliest == nil || exp.Before(*earliest) {
			earliest = &exp
		}
		return u, nil
	}
	base := requestBaseURL(r)
	out := make([]string, 0, len(req.URIs))
	for _, raw := range req.URIs {
		uri := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://"):
			out = append(out, uri)
			continue
		case strings.HasPrefix(uri, "gs://") || (strings.HasPrefix(uri, "s3://") && !s.inFilesBackend(uri)):
			// Registered reference buckets (refbuckets.go).
			signed, exp, ok, err := s.presignReference(ctx, pid, uri, ttl)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, store.Invalidf("cannot resolve %q: the bucket is not registered as a reference bucket for %s", uri, pid)
			}
			if earliest == nil || exp.Before(*earliest) {
				earliest = &exp
			}
			out = append(out, signed)
			continue
		case strings.HasPrefix(uri, "s3://"):
			if backend == nil || presigner == nil {
				return nil, store.Invalidf("cannot resolve %q: no S3 files backend is configured", uri)
			}
			u, err := url.Parse(uri)
			if err != nil || u.Host == "" {
				return nil, store.Invalidf("invalid uri %q", uri)
			}
			key := strings.TrimPrefix(u.Path, "/")
			if backend.URI(key) != "s3://"+u.Host+"/"+key || !strings.HasPrefix(key, backend.Key(pid+"/")) || strings.Contains(key, "..") {
				return nil, store.Invalidf("uri %q is not a file of project %s in the configured bucket", uri, pid)
			}
			signed, err := presign(key)
			if err != nil {
				return nil, err
			}
			out = append(out, signed)
			continue
		}
		digest := uri
		for _, p := range []string{"weft-file://", "weave-file://"} {
			digest = strings.TrimPrefix(digest, p)
		}
		if !fileDigestRe.MatchString(digest) {
			return nil, store.Invalidf("cannot resolve %q: expected a file digest, an s3:// uri or an http(s) URL", uri)
		}
		fi, err := s.store.StatFile(ctx, pid, digest)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, store.Invalidf("file %q not found in %s", digest, pid)
			}
			return nil, err
		}
		if fi.Location != "" && presigner != nil {
			signed, err := presign(fi.Location)
			if err != nil {
				return nil, err
			}
			out = append(out, signed)
			continue
		}
		q := url.Values{"project_id": {pid}, "digest": {digest}}
		out = append(out, base+"/file/content?"+q.Encode())
	}
	var exp *string
	if earliest != nil {
		v := earliest.UTC().Format(time.RFC3339)
		exp = &v
	}
	return map[string]any{"download_urls": out, "expires_at": exp}, nil
}

// requestBaseURL is the externally visible scheme://host of the request.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" && !strings.ContainsAny(h, "/ ,") {
		host = h
	}
	return scheme + "://" + host
}

type imageCreateReq struct {
	ProjectID    string         `json:"project_id"`
	Inputs       map[string]any `json:"inputs"`
	WBUserID     *string        `json:"wb_user_id"`
	TrackLLMCall *bool          `json:"track_llm_call"`
}

// imageCreate generates images through the routed model (OpenAI-compatible
// providers, Provider objects and runtimes; mock/* works offline) and, unless
// track_llm_call is false, records a weft.images.generate call.
func (s *Server) imageCreate(ctx context.Context, _ *http.Request, req *imageCreateReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.Inputs == nil {
		return nil, store.Invalidf("inputs are required")
	}
	if m, _ := req.Inputs["model"].(string); m == "" {
		return nil, store.Invalidf("inputs.model is required")
	}
	if p, _ := req.Inputs["prompt"].(string); p == "" {
		return nil, store.Invalidf("inputs.prompt is required")
	}
	var resp map[string]any
	var callID *string
	if req.TrackLLMCall == nil || *req.TrackLLMCall {
		var id string
		resp, id, err = s.engine.TrackedImage(ctx, pid, req.Inputs)
		if id != "" {
			callID = &id
		}
	} else {
		resp, err = s.llm.GenerateImage(ctx, pid, req.Inputs)
	}
	if err != nil {
		resp = map[string]any{"error": err.Error()}
	}
	return map[string]any{"response": resp, "weave_call_id": callID}, nil
}
