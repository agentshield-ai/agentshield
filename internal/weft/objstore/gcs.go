package objstore

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GCS issues Google Cloud Storage V4 signed URLs with a service-account key
// (RSA-SHA256, "GOOG4-RSA-SHA256"), using only the standard library.
// Reads go through signed URLs too, so the key needs no OAuth token exchange.
type GCS struct {
	bucket   string
	email    string
	key      *rsa.PrivateKey
	base     *url.URL
	http     *http.Client
	now      func() time.Time
	location string
}

// GCSConfig configures a GCS signer for one bucket.
type GCSConfig struct {
	Bucket string
	// ServiceAccountJSON is a service-account key file (type "service_account",
	// client_email, private_key).
	ServiceAccountJSON []byte
	// Endpoint defaults to https://storage.googleapis.com (path-style URLs).
	Endpoint string
	HTTP     *http.Client
	Now      func() time.Time
}

// ServiceAccount is the part of a key file that signing needs.
type ServiceAccount struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

// ParseServiceAccount reads a service-account key file.
func ParseServiceAccount(raw []byte) (string, *rsa.PrivateKey, error) {
	var sa ServiceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return "", nil, errors.New("gcs: the service-account key is not valid JSON")
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", nil, errors.New("gcs: the service-account key needs client_email and private_key")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", nil, errors.New("gcs: private_key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return "", nil, errors.New("gcs: private_key is not an RSA key")
		}
		key = rk
	} else if k, err2 := x509.ParsePKCS1PrivateKey(block.Bytes); err2 == nil {
		key = k
	} else {
		return "", nil, fmt.Errorf("gcs: parsing private_key: %w", err)
	}
	return sa.ClientEmail, key, nil
}

// NewGCS validates cfg and returns a signer.
func NewGCS(cfg GCSConfig) (*GCS, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("gcs: bucket is required")
	}
	email, key, err := ParseServiceAccount(cfg.ServiceAccountJSON)
	if err != nil {
		return nil, err
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://storage.googleapis.com"
	}
	base, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("gcs: invalid endpoint %q", endpoint)
	}
	g := &GCS{bucket: cfg.Bucket, email: email, key: key, base: base, http: cfg.HTTP, now: cfg.Now, location: "auto"}
	if g.http == nil {
		g.http = &http.Client{Timeout: 5 * time.Minute}
	}
	if g.now == nil {
		g.now = time.Now
	}
	return g, nil
}

// Bucket is the configured bucket.
func (g *GCS) Bucket() string { return g.bucket }

// Email is the signing service account.
func (g *GCS) Email() string { return g.email }

// URI renders an object as gs://bucket/key.
func (g *GCS) URI(key string) string { return "gs://" + g.bucket + "/" + key }

const gcsAlgorithm = "GOOG4-RSA-SHA256"

// PresignGet returns a V4 signed GET URL valid for ttl (1s to 7 days).
func (g *GCS) PresignGet(key string, ttl time.Duration) (string, time.Time, error) {
	return g.presign(http.MethodGet, key, ttl)
}

func (g *GCS) presign(method, key string, ttl time.Duration) (string, time.Time, error) {
	if ttl < time.Second || ttl > 7*24*time.Hour {
		return "", time.Time{}, fmt.Errorf("gcs: signed URL ttl must be between 1s and 7 days")
	}
	if key == "" {
		return "", time.Time{}, errors.New("gcs: object key is required")
	}
	u := *g.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + g.bucket + "/" + key
	u.RawPath = escapePath(u.Path)
	now := g.now().UTC()
	date := now.Format("20060102")
	scope := date + "/" + g.location + "/storage/goog4_request"
	q := url.Values{}
	q.Set("X-Goog-Algorithm", gcsAlgorithm)
	q.Set("X-Goog-Credential", g.email+"/"+scope)
	q.Set("X-Goog-Date", now.Format(amzDateFormat))
	q.Set("X-Goog-Expires", fmt.Sprint(int64(ttl/time.Second)))
	q.Set("X-Goog-SignedHeaders", "host")
	canonical := strings.Join([]string{
		method,
		u.EscapedPath(),
		canonicalQuery(q),
		"host:" + u.Host + "\n",
		"host",
		unsignedPayload,
	}, "\n")
	h := sha256.Sum256([]byte(canonical))
	toSign := gcsAlgorithm + "\n" + now.Format(amzDateFormat) + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	digest := sha256.Sum256([]byte(toSign))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gcs: signing: %w", err)
	}
	u.RawQuery = canonicalQuery(q) + "&X-Goog-Signature=" + hex.EncodeToString(sig)
	return u.String(), now.Add(ttl), nil
}

// Get downloads an object through a short-lived signed URL.
func (g *GCS) Get(ctx context.Context, key string) ([]byte, error) {
	signed, _, err := g.presign(http.MethodGet, key, time.Minute)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signed, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gcs get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gcs get %s: %s", key, errorBody(resp))
	}
	return io.ReadAll(resp.Body)
}
