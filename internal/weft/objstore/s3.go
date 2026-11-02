// Package objstore is a minimal S3-compatible object store client used as
// Weft's optional "bring your own bucket" backend for file contents.
//
// It implements PUT and GET of whole objects and presigned GET URLs, signed
// with AWS Signature Version 4 using only the standard library. It works with
// AWS S3 and S3-compatible stores (MinIO, Cloudflare R2, Ceph RGW, GCS
// interoperability mode, ...).
package objstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned by Get for a missing object.
var ErrNotFound = errors.New("object not found")

// Credentials are AWS-style static credentials.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// Config configures an S3 client.
type Config struct {
	// Bucket and Prefix come from an s3://bucket/prefix URL.
	Bucket string
	Prefix string
	// Endpoint is the service URL, e.g. https://s3.us-east-1.amazonaws.com or
	// http://127.0.0.1:9000 (MinIO). Empty means AWS for Region.
	Endpoint string
	Region   string
	// PathStyle addresses objects as endpoint/bucket/key instead of
	// bucket.endpoint/key. Defaults to true when Endpoint is set.
	PathStyle *bool
	Creds     Credentials
	// HTTP is the client used for requests; nil uses a client with a 5 minute timeout.
	HTTP *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
}

// S3 is an S3-compatible client bound to one bucket and key prefix.
type S3 struct {
	cfg       Config
	base      *url.URL
	pathStyle bool
}

// ParseURL splits an s3://bucket/prefix URL.
func ParseURL(raw string) (bucket, prefix string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "s3" || u.Host == "" {
		return "", "", fmt.Errorf("files backend must look like s3://bucket/prefix, got %q", raw)
	}
	return u.Host, strings.Trim(u.Path, "/"), nil
}

// New validates cfg and returns a client.
func New(cfg Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3: bucket is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.Creds.AccessKeyID == "" || cfg.Creds.SecretAccessKey == "" {
		return nil, errors.New("s3: access key id and secret access key are required (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY)")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	endpoint := cfg.Endpoint
	pathStyle := endpoint != ""
	if cfg.PathStyle != nil {
		pathStyle = *cfg.PathStyle
	}
	if endpoint == "" {
		endpoint = "https://s3." + cfg.Region + ".amazonaws.com"
		if cfg.Region == "us-east-1" {
			endpoint = "https://s3.amazonaws.com"
		}
	}
	base, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("s3: invalid endpoint %q", endpoint)
	}
	if !pathStyle {
		base.Host = cfg.Bucket + "." + base.Host
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	return &S3{cfg: cfg, base: base, pathStyle: pathStyle}, nil
}

// Name identifies the backend kind.
func (s *S3) Name() string { return "s3" }

// Bucket is the configured bucket.
func (s *S3) Bucket() string { return s.cfg.Bucket }

// Key joins the configured prefix and a relative object key.
func (s *S3) Key(rel string) string {
	rel = strings.TrimLeft(rel, "/")
	if s.cfg.Prefix == "" {
		return rel
	}
	return s.cfg.Prefix + "/" + rel
}

// URI renders a key as s3://bucket/key.
func (s *S3) URI(key string) string { return "s3://" + s.cfg.Bucket + "/" + key }

// objectURL is the request URL of a (full) key.
func (s *S3) objectURL(key string) *url.URL {
	u := *s.base
	p := strings.TrimRight(u.Path, "/")
	if s.pathStyle {
		p += "/" + s.cfg.Bucket
	}
	u.Path = p + "/" + key
	u.RawPath = escapePath(u.Path)
	return &u
}

// Put uploads content to key (a full key; see Key).
func (s *S3) Put(ctx context.Context, key string, content []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key).String(), bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(content))
	req.Header.Set("Content-Type", "application/octet-stream")
	s.sign(req, content)
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("s3 put %s: %s", key, errorBody(resp))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Get downloads key.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key).String(), nil)
	if err != nil {
		return nil, err
	}
	s.sign(req, nil)
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("s3 get %s: %s", key, errorBody(resp))
	}
	return io.ReadAll(resp.Body)
}

// PresignGet returns a presigned GET URL valid for ttl (1s to 7 days).
func (s *S3) PresignGet(key string, ttl time.Duration) (string, time.Time, error) {
	if ttl < time.Second || ttl > 7*24*time.Hour {
		return "", time.Time{}, fmt.Errorf("s3: presign ttl must be between 1s and 7 days")
	}
	u := s.objectURL(key)
	now := s.cfg.Now().UTC()
	q := url.Values{}
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", s.cfg.Creds.AccessKeyID+"/"+s.scope(now))
	q.Set("X-Amz-Date", now.Format(amzDateFormat))
	q.Set("X-Amz-Expires", fmt.Sprint(int64(ttl/time.Second)))
	q.Set("X-Amz-SignedHeaders", "host")
	if s.cfg.Creds.SessionToken != "" {
		q.Set("X-Amz-Security-Token", s.cfg.Creds.SessionToken)
	}
	canonical := strings.Join([]string{
		http.MethodGet,
		u.EscapedPath(),
		canonicalQuery(q),
		"host:" + u.Host + "\n",
		"host",
		unsignedPayload,
	}, "\n")
	u.RawQuery = canonicalQuery(q) + "&X-Amz-Signature=" + s.signature(now, canonical)
	return u.String(), now.Add(ttl), nil
}

const (
	algorithm       = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

func (s *S3) scope(t time.Time) string {
	return t.Format("20060102") + "/" + s.cfg.Region + "/s3/aws4_request"
}

// sign adds SigV4 headers (Authorization, x-amz-date, x-amz-content-sha256).
func (s *S3) sign(req *http.Request, payload []byte) {
	now := s.cfg.Now().UTC()
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Date", now.Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.cfg.Creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.cfg.Creds.SessionToken)
	}
	names := []string{"host"}
	vals := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
			vals[lk] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery(req.URL.Query()),
		ch.String(),
		signed,
		payloadHash,
	}, "\n")
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, s.cfg.Creds.AccessKeyID, s.scope(now), signed, s.signature(now, canonical)))
}

func (s *S3) signature(t time.Time, canonicalRequest string) string {
	h := sha256.Sum256([]byte(canonicalRequest))
	toSign := algorithm + "\n" + t.Format(amzDateFormat) + "\n" + s.scope(t) + "\n" + hex.EncodeToString(h[:])
	k := hmacSHA256([]byte("AWS4"+s.cfg.Creds.SecretAccessKey), t.Format("20060102"))
	k = hmacSHA256(k, s.cfg.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, toSign))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// uriEncode percent-encodes everything except RFC 3986 unreserved characters
// (and '/' when keepSlash), as SigV4 requires.
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func escapePath(p string) string { return uriEncode(p, true) }

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

func errorBody(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	msg := strings.TrimSpace(string(raw))
	if i := strings.Index(msg, "<Message>"); i >= 0 {
		if j := strings.Index(msg[i:], "</Message>"); j > 0 {
			msg = msg[i+9 : i+j]
		}
	}
	return fmt.Sprintf("%d %s", resp.StatusCode, msg)
}
