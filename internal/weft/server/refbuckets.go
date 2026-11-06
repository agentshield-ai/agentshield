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

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
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
// accepted) or, with no secret, from the AWS_* environment variables (only
// when RefBucketsEnvCredentials allows it; see bucketClient).
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

// RefBucketsEnvCredentials lets an S3 registration without secret_name use
// the server's AWS_* environment credentials. It is off by default: an admin
// could otherwise point a bucket at any endpoint and receive the server's
// access key id and session token in presigned URLs. Even when on, it applies
// only to AWS itself (no custom endpoint). Set from
// --ref-buckets-env-credentials in cmd/weft.
var RefBucketsEnvCredentials bool

// bucketClient builds a signer for a registration.
func (s *Server) bucketClient(ctx context.Context, b *store.ReferenceBucket) (bucketSigner, error) {
	switch b.Provider {
	case store.BucketS3:
		if b.SecretName == "" && (!RefBucketsEnvCredentials || b.Endpoint != "") {
			return nil, store.Invalidf("secret_name is required: store the bucket's credentials as a secret (Settings → Secrets)")
		}
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

// bucketTestClient fetches a presigned URL for a registered bucket. The bucket
// endpoint comes from the API, so the request goes through the egress guard.
var bucketTestClient = egress.NewClient(15*time.Second, false)

// bucketCheck is one part of a bucket test: reading the test object, or the
// bucket's CORS rules for the Weft origin. Error is nil when OK.
type bucketCheck struct {
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
	// Origin is the browser origin the CORS check used.
	Origin string `json:"origin,omitempty"`
}

// bucketTestResult is what /reference_buckets/test returns and what create
// requires to pass: ok only when the test object could be read and the
// bucket's CORS rules let the Weft origin GET it.
type bucketTestResult struct {
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
	// Read is reading the first byte of test_object_key through a presigned
	// URL; null when the test could not start (bad credentials or request).
	Read *bucketCheck `json:"read"`
	// CORS is the browser check for the origin: a preflight (OPTIONS with
	// Access-Control-Request-Method: GET) and the Access-Control-Allow-Origin
	// of the GET itself. Null when the bucket could not be reached.
	CORS *bucketCheck `json:"cors"`
}

func strPtr(s string) *string { return &s }

// bucketTestFailed reports a test that could not start.
func bucketTestFailed(msg string) *bucketTestResult {
	return &bucketTestResult{OK: false, Error: strPtr(msg)}
}

// bucketTestOrigin is the browser origin whose access the CORS check tests:
// the Origin header of the request (the UI's own origin when the UI calls),
// otherwise this server's externally visible scheme://host.
func bucketTestOrigin(r *http.Request) string {
	if o := strings.TrimSpace(r.Header.Get("Origin")); o != "" && o != "null" {
		if u, err := url.Parse(o); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Path == "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return requestBaseURL(r)
}

// bucketErrMessage extracts <Message> from an S3/GCS XML error body.
func bucketErrMessage(body []byte) string {
	msg := strings.TrimSpace(string(body))
	if i := strings.Index(msg, "<Message>"); i >= 0 {
		if j := strings.Index(msg[i:], "</Message>"); j > 0 {
			msg = msg[i+9 : i+j]
		}
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// allowsOrigin reports whether an Access-Control-Allow-Origin value admits origin.
func allowsOrigin(acao, origin string) bool {
	acao = strings.TrimSpace(acao)
	return acao == "*" || strings.EqualFold(strings.TrimSuffix(acao, "/"), origin)
}

// testBucket reads the first byte of the test object through a presigned URL,
// the way a browser would fetch it, and checks that the bucket's CORS rules
// let origin GET it. One readable object proves both the credentials and the
// CORS setup. Errors are request validation errors; test failures are
// reported in the result.
func (s *Server) testBucket(ctx context.Context, b *store.ReferenceBucket, origin string) (*bucketTestResult, error) {
	c, err := s.bucketClient(ctx, b)
	if err != nil {
		var inv *store.InvalidError
		if errors.As(err, &inv) {
			return bucketTestFailed(inv.Msg), nil
		}
		return nil, err
	}
	if b.TestObjectKey == "" {
		return bucketTestFailed("test_object_key (the key of an existing object) is required to test the bucket"), nil
	}
	if !store.BucketPrefixCovers(b.Prefix, b.TestObjectKey) {
		return bucketTestFailed(fmt.Sprintf("test_object_key must start with the prefix %q", b.Prefix)), nil
	}
	u, _, err := c.PresignGet(b.TestObjectKey, time.Minute)
	if err != nil {
		return bucketTestFailed(err.Error()), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Origin", origin)
	resp, err := bucketTestClient.Do(req)
	if err != nil {
		msg := fmt.Sprintf("cannot reach the bucket: %v", egress.Sanitize(err))
		return &bucketTestResult{Error: strPtr(msg), Read: &bucketCheck{Error: strPtr(msg)}}, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	res := &bucketTestResult{Read: &bucketCheck{OK: true}}
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusNotFound:
		res.Read = &bucketCheck{Error: strPtr(fmt.Sprintf("test object %s://%s/%s was not found", b.URIScheme(), b.Bucket, b.TestObjectKey))}
	default:
		res.Read = &bucketCheck{Error: strPtr(fmt.Sprintf("reading the test object failed: %d %s", resp.StatusCode, bucketErrMessage(body)))}
	}
	res.CORS = s.checkBucketCORS(ctx, u, origin, resp)
	res.OK = res.Read.OK && res.CORS.OK
	var msgs []string
	if res.Read.Error != nil {
		msgs = append(msgs, *res.Read.Error)
	}
	if res.CORS.Error != nil {
		msgs = append(msgs, *res.CORS.Error)
	}
	if len(msgs) > 0 {
		res.Error = strPtr(strings.Join(msgs, "; "))
	}
	return res, nil
}

// checkBucketCORS checks the bucket's CORS rules for origin the way a browser
// meets them: a preflight (OPTIONS with Origin and
// Access-Control-Request-Method: GET; S3 and GCS answer it without
// credentials from the bucket's CORS configuration) must allow the origin and
// GET, and the GET response (got, when the object was read) must carry an
// Access-Control-Allow-Origin admitting the origin.
func (s *Server) checkBucketCORS(ctx context.Context, presigned, origin string, got *http.Response) *bucketCheck {
	out := &bucketCheck{Origin: origin}
	fail := func(format string, a ...any) *bucketCheck {
		out.Error = strPtr("CORS: " + fmt.Sprintf(format, a...) +
			fmt.Sprintf(" (allow GET and HEAD from %s in the bucket's CORS configuration)", origin))
		return out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, presigned, nil)
	if err != nil {
		return fail("%v", err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	resp, err := bucketTestClient.Do(req)
	if err != nil {
		return fail("the preflight request failed: %v", egress.Sanitize(err))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	acao := resp.Header.Get("Access-Control-Allow-Origin")
	switch {
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		msg := bucketErrMessage(body)
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fail("the bucket refused a preflight from %s: %d %s", origin, resp.StatusCode, msg)
	case acao == "":
		return fail("the preflight response from the bucket has no Access-Control-Allow-Origin for %s", origin)
	case !allowsOrigin(acao, origin):
		return fail("the bucket allows origin %s, not %s", acao, origin)
	}
	if m := resp.Header.Get("Access-Control-Allow-Methods"); m != "" {
		ok := false
		for _, v := range strings.Split(m, ",") {
			if strings.EqualFold(strings.TrimSpace(v), http.MethodGet) || strings.TrimSpace(v) == "*" {
				ok = true
			}
		}
		if !ok {
			return fail("the bucket allows %s from %s, not GET", m, origin)
		}
	}
	if got != nil && (got.StatusCode == http.StatusOK || got.StatusCode == http.StatusPartialContent) {
		if v := got.Header.Get("Access-Control-Allow-Origin"); !allowsOrigin(v, origin) {
			if v == "" {
				return fail("the object's GET response has no Access-Control-Allow-Origin for %s", origin)
			}
			return fail("the object's GET response allows origin %s, not %s", v, origin)
		}
	}
	out.OK = true
	return out
}

func (s *Server) refBucketTest(ctx context.Context, r *http.Request, req *refBucketReq) (any, error) {
	if !callerIsAdmin(r) {
		return nil, forbidden("testing reference buckets needs a server admin")
	}
	b := req.ReferenceBucket
	if err := store.NormalizeReferenceBucket(&b); err != nil {
		return nil, err
	}
	return s.testBucket(ctx, &b, bucketTestOrigin(r))
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
		res, err := s.testBucket(ctx, &b, bucketTestOrigin(r))
		if err != nil {
			return nil, err
		}
		if !res.OK {
			return nil, store.Invalidf("%s", *res.Error)
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
