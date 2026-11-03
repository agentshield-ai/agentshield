// Package gcstest provides an in-process fake Google Cloud Storage XML API
// that verifies V4 signed URLs (GOOG4-RSA-SHA256), for tests.
package gcstest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fake serves objects from memory to requests carrying a valid V4 signature
// by the service account it was created with.
type Fake struct {
	Email string
	Key   *rsa.PrivateKey

	mu      sync.Mutex
	Objects map[string][]byte // "/bucket/object" -> content
	Rejects int
	// CORS holds each bucket's CORS configuration (gcloud storage buckets
	// update --cors-file), keyed by bucket name; SetCORS sets it.
	CORS map[string][]CORSEntry
}

// CORSEntry is one entry of a GCS bucket CORS configuration.
type CORSEntry struct {
	Origin         []string `json:"origin"` // exact origins or "*"
	Method         []string `json:"method"`
	ResponseHeader []string `json:"responseHeader"`
	MaxAgeSeconds  int      `json:"maxAgeSeconds"`
}

// SetCORS replaces bucket's CORS configuration; no entries deletes it.
func (f *Fake) SetCORS(bucket string, entries ...CORSEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CORS == nil {
		f.CORS = map[string][]CORSEntry{}
	}
	if len(entries) == 0 {
		delete(f.CORS, bucket)
		return
	}
	f.CORS[bucket] = entries
}

// corsMatch returns the first entry of bucket allowing origin and method.
func (f *Fake) corsMatch(path, origin, method string) *CORSEntry {
	bucket, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.CORS[bucket] {
		e := &f.CORS[bucket][i]
		okOrigin, okMethod := false, false
		for _, o := range e.Origin {
			okOrigin = okOrigin || o == "*" || o == origin
		}
		for _, m := range e.Method {
			okMethod = okMethod || m == "*" || strings.EqualFold(m, method)
		}
		if okOrigin && okMethod {
			return e
		}
	}
	return nil
}

// corsHeaders sets the CORS response headers GCS sends for a matched entry.
func corsHeaders(w http.ResponseWriter, e *CORSEntry, origin string, preflight bool) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	for _, o := range e.Origin {
		if o == "*" {
			h.Set("Access-Control-Allow-Origin", "*")
		}
	}
	h.Add("Vary", "Origin")
	if len(e.ResponseHeader) > 0 {
		if preflight {
			h.Set("Access-Control-Allow-Headers", strings.Join(e.ResponseHeader, ","))
		} else {
			h.Set("Access-Control-Expose-Headers", strings.Join(e.ResponseHeader, ","))
		}
	}
	if preflight {
		h.Set("Access-Control-Allow-Methods", strings.Join(e.Method, ","))
		if e.MaxAgeSeconds > 0 {
			h.Set("Access-Control-Max-Age", strconv.Itoa(e.MaxAgeSeconds))
		}
	}
}

// NewFake starts a fake GCS with a fresh service-account key. It is closed
// when the test ends.
func NewFake(t testing.TB) (*Fake, *httptest.Server) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &Fake{Email: "weft-test@example-project.iam.gserviceaccount.com", Key: key, Objects: map[string][]byte{}}
	hs := httptest.NewServer(f)
	t.Cleanup(hs.Close)
	return f, hs
}

// ServiceAccountJSON is a key file for the fake's service account.
func (f *Fake) ServiceAccountJSON() []byte {
	der, _ := x509.MarshalPKCS8PrivateKey(f.Key)
	b, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "example-project", "client_email": f.Email,
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	return b
}

func enc(s string, slash bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || strings.IndexByte("-_.~", c) >= 0 || (slash && c == '/') {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// Verify checks a V4 signed URL independently of the client; "" is valid.
func (f *Fake) Verify(r *http.Request) string {
	q := r.URL.Query()
	if q.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" {
		return "missing or bad X-Goog-Algorithm"
	}
	date := q.Get("X-Goog-Date")
	ts, err := time.Parse("20060102T150405Z", date)
	if err != nil {
		return "bad date"
	}
	exp, err := strconv.Atoi(q.Get("X-Goog-Expires"))
	if err != nil || exp < 1 || exp > 604800 || time.Since(ts) > time.Duration(exp)*time.Second {
		return "expired"
	}
	cred := strings.Split(q.Get("X-Goog-Credential"), "/")
	if len(cred) != 5 || cred[0] != f.Email || cred[1] != date[:8] || cred[3] != "storage" || cred[4] != "goog4_request" {
		return "bad credential"
	}
	if q.Get("X-Goog-SignedHeaders") != "host" {
		return "only host may be signed"
	}
	sig, err := hex.DecodeString(q.Get("X-Goog-Signature"))
	if err != nil {
		return "bad signature encoding"
	}
	q.Del("X-Goog-Signature")
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		qs = append(qs, enc(k, false)+"="+enc(q.Get(k), false))
	}
	creq := r.Method + "\n" + enc(r.URL.Path, true) + "\n" + strings.Join(qs, "&") + "\nhost:" + r.Host + "\n\nhost\nUNSIGNED-PAYLOAD"
	h := sha256.Sum256([]byte(creq))
	sts := "GOOG4-RSA-SHA256\n" + date + "\n" + strings.Join(cred[1:], "/") + "\n" + hex.EncodeToString(h[:])
	d := sha256.Sum256([]byte(sts))
	if rsa.VerifyPKCS1v15(&f.Key.PublicKey, crypto.SHA256, d[:], sig) != nil {
		return "signature mismatch"
	}
	return ""
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if r.Method == http.MethodOptions {
		// Preflights are answered without authentication from the bucket's
		// CORS configuration; when nothing matches, GCS answers 200 without
		// CORS headers, which the browser treats as a refusal.
		if e := f.corsMatch(r.URL.Path, origin, r.Header.Get("Access-Control-Request-Method")); e != nil && origin != "" {
			corsHeaders(w, e, origin, true)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if origin != "" {
		if e := f.corsMatch(r.URL.Path, origin, r.Method); e != nil {
			corsHeaders(w, e, origin, false)
		}
	}
	if msg := f.Verify(r); msg != "" {
		f.mu.Lock()
		f.Rejects++
		f.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error><Code>SignatureDoesNotMatch</Code><Message>"+msg+"</Message></Error>")
		return
	}
	f.mu.Lock()
	b, ok := f.Objects[r.URL.Path]
	f.mu.Unlock()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>No such object</Message></Error>")
		return
	}
	_, _ = w.Write(b)
}
