package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/objstore"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Reference buckets: customer S3 and GCS buckets registered for a team
// (entity) or a project, whose objects /files/resolve_url presigns so media
// referenced by URI (s3://bucket/key, gs://bucket/key) renders without being
// copied into Weft. Registering, testing and deleting need a server admin;
// listing the buckets a project may use needs viewer on it.
//
//	POST /reference_buckets/create  register (tests test_object_key first unless skip_test)
//	POST /reference_buckets/test    check credentials and read access without saving
//	GET|POST /reference_buckets/list  ?entity= / ?project_id= (or the same in the body)
//	POST /reference_buckets/delete  {"id": ...}

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/reference_buckets/create", handleIAM(s.refBucketCreate))
		r.Post("/reference_buckets/test", handleIAM(s.refBucketTest))
		r.Get("/reference_buckets/list", s.refBucketList)
		r.Post("/reference_buckets/list", s.refBucketList)
		r.Post("/reference_buckets/delete", handleIAM(s.refBucketDelete))
	})
}

type refBucketReq struct {
	store.ReferenceBucket
	// SkipTest registers without reading test_object_key.
	SkipTest bool `json:"skip_test"`
}

// bucketSigner presigns and reads objects of one registered bucket.
type bucketSigner interface {
	PresignGet(key string, ttl time.Duration) (string, time.Time, error)
}

// s3SecretCreds reads S3 credentials from a stored secret (JSON with
// access_key_id/secret_access_key/session_token, AWS-style key names also
// accepted) or, with no secret, from the AWS_* environment variables.
func (s *Server) s3SecretCreds(ctx context.Context, name string) (objstore.Credentials, error) {
	if name == "" {
		c := objstore.Credentials{AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken: os.Getenv("AWS_SESSION_TOKEN")}
		if c.AccessKeyID == "" || c.SecretAccessKey == "" {
			return c, store.Invalidf("no secret_name given and AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY are not set")
		}
		return c, nil
	}
	raw, ok, err := s.store.GetSecret(ctx, name)
	if err != nil {
		return objstore.Credentials{}, err
	}
	if !ok {
		return objstore.Credentials{}, store.Invalidf("secret %q does not exist (Settings → Secrets)", name)
	}
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) != nil {
		return objstore.Credentials{}, store.Invalidf(`secret %q must be JSON {"access_key_id": ..., "secret_access_key": ...}`, name)
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(m[k]); v != "" {
				return v
			}
		}
		return ""
	}
	c := objstore.Credentials{
		AccessKeyID:     pick("access_key_id", "aws_access_key_id", "AccessKeyId", "AWS_ACCESS_KEY_ID"),
		SecretAccessKey: pick("secret_access_key", "aws_secret_access_key", "SecretAccessKey", "AWS_SECRET_ACCESS_KEY"),
		SessionToken:    pick("session_token", "aws_session_token", "SessionToken", "AWS_SESSION_TOKEN"),
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return c, store.Invalidf("secret %q has no access_key_id / secret_access_key", name)
	}
	return c, nil
}

// bucketClient builds a signer for a registration.
func (s *Server) bucketClient(ctx context.Context, b *store.ReferenceBucket) (bucketSigner, error) {
	switch b.Provider {
	case store.BucketS3:
		creds, err := s.s3SecretCreds(ctx, b.SecretName)
		if err != nil {
			return nil, err
		}
		region := b.Region
		if region == "" {
			region = os.Getenv("AWS_REGION")
		}
		c, err := objstore.New(objstore.Config{Bucket: b.Bucket, Endpoint: b.Endpoint, Region: region, PathStyle: b.PathStyle, Creds: creds})
		if err != nil {
			return nil, store.Invalidf("%v", err)
		}
		return c, nil
	case store.BucketGCS:
		raw, ok, err := s.store.GetSecret(ctx, b.SecretName)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, store.Invalidf("secret %q does not exist (Settings → Secrets)", b.SecretName)
		}
		c, err := objstore.NewGCS(objstore.GCSConfig{Bucket: b.Bucket, ServiceAccountJSON: []byte(raw), Endpoint: b.Endpoint})
		if err != nil {
			return nil, store.Invalidf("secret %q: %v", b.SecretName, err)
		}
		return c, nil
	}
	return nil, store.Invalidf("unknown provider %q", b.Provider)
}

var bucketTestClient = &http.Client{Timeout: 15 * time.Second}

// testBucket reads the first byte of key through a presigned URL, the way a
// browser would fetch it.
func (s *Server) testBucket(ctx context.Context, b *store.ReferenceBucket) error {
	c, err := s.bucketClient(ctx, b)
	if err != nil {
		return err
	}
	if b.TestObjectKey == "" {
		return store.Invalidf("test_object_key (the key of an existing object) is required to test the bucket")
	}
	if !strings.HasPrefix(b.TestObjectKey, b.Prefix) {
		return store.Invalidf("test_object_key must start with the prefix %q", b.Prefix)
	}
	u, _, err := c.PresignGet(b.TestObjectKey, time.Minute)
	if err != nil {
		return store.Invalidf("%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := bucketTestClient.Do(req)
	if err != nil {
		return store.Invalidf("cannot reach the bucket: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return store.Invalidf("test object %s://%s/%s was not found", b.URIScheme(), b.Bucket, b.TestObjectKey)
	default:
		msg := strings.TrimSpace(string(body))
		if i := strings.Index(msg, "<Message>"); i >= 0 {
			if j := strings.Index(msg[i:], "</Message>"); j > 0 {
				msg = msg[i+9 : i+j]
			}
		}
		return store.Invalidf("reading the test object failed: %d %s", resp.StatusCode, msg)
	}
}

func (s *Server) refBucketTest(ctx context.Context, r *http.Request, req *refBucketReq) (any, error) {
	if !callerIsAdmin(r) {
		return nil, forbidden("testing reference buckets needs a server admin")
	}
	b := req.ReferenceBucket
	if err := store.NormalizeReferenceBucket(&b); err != nil {
		return nil, err
	}
	if err := s.testBucket(ctx, &b); err != nil {
		var inv *store.InvalidError
		if errors.As(err, &inv) {
			return map[string]any{"ok": false, "error": inv.Msg}, nil
		}
		return nil, err
	}
	return map[string]any{"ok": true, "error": nil}, nil
}

func (s *Server) refBucketCreate(ctx context.Context, r *http.Request, req *refBucketReq) (any, error) {
	if !callerIsAdmin(r) {
		return nil, forbidden("registering reference buckets needs a server admin")
	}
	b := req.ReferenceBucket
	if err := store.NormalizeReferenceBucket(&b); err != nil {
		return nil, err
	}
	if _, err := s.bucketClient(ctx, &b); err != nil { // credentials must at least parse
		return nil, err
	}
	verified := false
	if !req.SkipTest {
		if err := s.testBucket(ctx, &b); err != nil {
			return nil, err
		}
		verified = true
	}
	b.CreatedBy = userFrom(r, nil)
	out, err := s.store.CreateReferenceBucket(ctx, &b, verified)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bucket": out}, nil
}

func (s *Server) refBucketDelete(ctx context.Context, r *http.Request, req *struct {
	ID string `json:"id"`
}) (any, error) {
	if !callerIsAdmin(r) {
		return nil, forbidden("deleting reference buckets needs a server admin")
	}
	if req.ID == "" {
		return nil, store.Invalidf("id is required")
	}
	if err := s.store.DeleteReferenceBucket(ctx, req.ID); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

func (s *Server) refBucketList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entity    string `json:"entity"`
		ProjectID string `json:"project_id"`
	}
	if r.Method == http.MethodPost {
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	}
	if v := r.URL.Query().Get("entity"); v != "" {
		req.Entity = v
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		req.ProjectID = v
	}
	if req.ProjectID != "" {
		pid, err := project(req.ProjectID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if callerRole(r, pid).Rank() < store.RoleViewer.Rank() {
			writeError(w, http.StatusForbidden, "no access to project "+pid)
			return
		}
		req.ProjectID = pid
	} else if !callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "listing buckets without a project_id needs a server admin")
		return
	}
	out, err := s.store.ListReferenceBuckets(r.Context(), req.Entity, req.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"buckets": out})
}

// presignReference presigns s3:// or gs:// uri when it points into a bucket
// registered for the project (or its team). ok is false when no
// registration covers the uri.
func (s *Server) presignReference(ctx context.Context, pid, uri string, ttl time.Duration) (string, time.Time, bool, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return "", time.Time{}, false, store.Invalidf("invalid uri %q", uri)
	}
	provider := store.BucketS3
	if u.Scheme == "gs" {
		provider = store.BucketGCS
	}
	key := strings.TrimPrefix(u.Path, "/")
	if key == "" || strings.Contains(key, "..") {
		return "", time.Time{}, false, store.Invalidf("invalid object key in %q", uri)
	}
	b, err := s.store.ReferenceBucketFor(ctx, pid, provider, u.Host, key)
	if errors.Is(err, store.ErrNotFound) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	c, err := s.bucketClient(ctx, b)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("reference bucket %s://%s: %w", b.URIScheme(), b.Bucket, err)
	}
	signed, exp, err := c.PresignGet(key, ttl)
	if err != nil {
		return "", time.Time{}, false, err
	}
	return signed, exp, true, nil
}

// inFilesBackend reports whether an s3:// uri names the files backend bucket.
func (s *Server) inFilesBackend(uri string) bool {
	b := s.store.FileBackend()
	if b == nil {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	return strings.HasPrefix(b.URI(""), "s3://"+u.Host+"/")
}
