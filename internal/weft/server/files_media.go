package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// POST /files/resolve_url turns file references into download URLs, and
// POST /image/create generates images (playground image generation).

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
	// PerURIErrors (Weft extension) reports unresolvable URIs per URI
	// instead of failing the whole request with a 400: their download_urls
	// entry is "" and "errors" (aligned with uris) says why.
	PerURIErrors bool `json:"per_uri_errors"`
}

var fileDigestRe = regexp.MustCompile(`^[A-Za-z0-9]{20,64}$`)

// Codes of per-URI resolve errors (per_uri_errors).
const (
	resolveBucketNotRegistered = "bucket_not_registered"
	resolveNoFilesBackend      = "no_files_backend"
	resolveNotProjectFile      = "not_project_file"
	resolveFileNotFound        = "file_not_found"
	resolveInvalidURI          = "invalid_uri"
	resolveBucketError         = "bucket_error"
)

// resolveError is why one URI could not be resolved. cause, when set, is
// returned as is without per_uri_errors (credential and signing failures of
// a registered bucket), otherwise the message is a 400.
type resolveError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	cause   error
}

func (e *resolveError) Error() string { return e.Message }

func resolveErrf(code, format string, a ...any) *resolveError {
	return &resolveError{Code: code, Message: fmt.Sprintf(format, a...)}
}

// filesResolveURL resolves each uri to a download URL:
//   - a file digest (as returned by /file/create; "weft-file://<digest>" is
//     accepted too): a presigned URL when the file
//     lives in a presigning backend (S3), otherwise this server's
//     GET /api/v1/files/{digest} URL;
//   - s3://<backend bucket>/<key> under this project's prefix: a presigned URL;
//   - s3:// and gs:// URIs in a reference bucket registered for the project
//     or its team (refbuckets.go): a presigned (S3) or V4 signed (GCS) URL;
//   - http(s) URLs: returned unchanged.
//
// expires_at is the earliest expiry of the presigned URLs, or null. One
// unresolvable uri fails the request (400) unless per_uri_errors asks for
// per-URI results.
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
	expires := func(exp time.Time) {
		if earliest == nil || exp.Before(*earliest) {
			earliest = &exp
		}
	}
	presign := func(key string) (string, error) {
		u, exp, err := presigner.PresignGet(key, ttl)
		if err != nil {
			return "", err
		}
		expires(exp)
		return u, nil
	}
	base := requestBaseURL(r)
	resolve := func(raw string) (string, error) {
		uri := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://"):
			return uri, nil
		case strings.HasPrefix(uri, "gs://") || (strings.HasPrefix(uri, "s3://") && !s.inFilesBackend(uri)):
			// Registered reference buckets (refbuckets.go).
			signed, exp, ok, err := s.presignReference(ctx, pid, uri, ttl)
			var inv *store.InvalidError
			switch {
			case errors.As(err, &inv) && strings.HasPrefix(inv.Msg, "invalid "):
				return "", resolveErrf(resolveInvalidURI, "%s", inv.Msg)
			case err != nil:
				return "", &resolveError{Code: resolveBucketError, Message: err.Error(), cause: err}
			case !ok:
				return "", resolveErrf(resolveBucketNotRegistered, "cannot resolve %q: the bucket is not registered as a reference bucket for %s", uri, pid)
			}
			expires(exp)
			return signed, nil
		case strings.HasPrefix(uri, "s3://"):
			if backend == nil || presigner == nil {
				return "", resolveErrf(resolveNoFilesBackend, "cannot resolve %q: no S3 files backend is configured", uri)
			}
			u, err := url.Parse(uri)
			if err != nil || u.Host == "" {
				return "", resolveErrf(resolveInvalidURI, "invalid uri %q", uri)
			}
			key := strings.TrimPrefix(u.Path, "/")
			if backend.URI(key) != "s3://"+u.Host+"/"+key || !strings.HasPrefix(key, backend.Key(pid+"/")) || strings.Contains(key, "..") {
				return "", resolveErrf(resolveNotProjectFile, "uri %q is not a file of project %s in the configured bucket", uri, pid)
			}
			return presign(key)
		}
		digest := strings.TrimPrefix(uri, "weft-file://")
		if !fileDigestRe.MatchString(digest) {
			return "", resolveErrf(resolveInvalidURI, "cannot resolve %q: expected a file digest, an s3:// uri or an http(s) URL", uri)
		}
		fi, err := s.store.StatFile(ctx, pid, digest)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", resolveErrf(resolveFileNotFound, "file %q not found in %s", digest, pid)
			}
			return "", err
		}
		if fi.Location != "" && presigner != nil {
			return presign(fi.Location)
		}
		return nativeFileURL(base, pid, digest), nil
	}
	out := make([]string, 0, len(req.URIs))
	var perURI []*resolveError
	if req.PerURIErrors {
		perURI = make([]*resolveError, 0, len(req.URIs))
	}
	for _, raw := range req.URIs {
		u, err := resolve(raw)
		var re *resolveError
		switch {
		case err == nil:
			out = append(out, u)
			if perURI != nil {
				perURI = append(perURI, nil)
			}
		case errors.As(err, &re) && req.PerURIErrors:
			out = append(out, "")
			perURI = append(perURI, re)
		case errors.As(err, &re) && re.cause != nil:
			return nil, re.cause
		case errors.As(err, &re):
			return nil, store.Invalidf("%s", re.Message)
		default:
			return nil, err
		}
	}
	var exp *string
	if earliest != nil {
		v := earliest.UTC().Format(time.RFC3339)
		exp = &v
	}
	res := map[string]any{"download_urls": out, "expires_at": exp}
	if req.PerURIErrors {
		res["errors"] = perURI
	}
	return res, nil
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
	UserID       *string        `json:"user_id"`
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
	return map[string]any{"response": resp, "call_id": callID}, nil
}
