// Package s3test provides an in-process fake S3 server that verifies AWS
// Signature Version 4 on every request, for tests.
package s3test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fake is an in-process S3 that verifies SigV4 (header and presigned) with
// an independent implementation and stores objects in memory.
type Fake struct {
	AccessKeyID, Secret, Region string

	mu      sync.Mutex
	Objects map[string][]byte
	Puts    int
	Rejects int
	// Requests records "METHOD path" of every request.
	Requests []string
}

// NewFake starts a fake S3 accepting requests signed with the given
// credentials for region. It is closed when the test ends.
func NewFake(t testing.TB, accessKeyID, secretAccessKey, region string) (*Fake, *httptest.Server) {
	f := &Fake{AccessKeyID: accessKeyID, Secret: secretAccessKey, Region: region, Objects: map[string][]byte{}}
	hs := httptest.NewServer(f)
	t.Cleanup(hs.Close)
	return f, hs
}

func vHMAC(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func vEncode(s string, slash bool) string {
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

// Verify checks the request's SigV4 signature (Authorization header or
// presigned query) with an implementation independent of the client; it
// returns "" when valid.
func (f *Fake) Verify(r *http.Request, body []byte) string {
	q := r.URL.Query()
	var date, credential, signedHeaders, signature, payloadHash string
	presigned := q.Get("X-Amz-Algorithm") != ""
	if presigned {
		date, credential, signedHeaders, signature = q.Get("X-Amz-Date"), q.Get("X-Amz-Credential"), q.Get("X-Amz-SignedHeaders"), q.Get("X-Amz-Signature")
		payloadHash = "UNSIGNED-PAYLOAD"
		ts, err := time.Parse("20060102T150405Z", date)
		if err != nil {
			return "bad date"
		}
		var exp int
		if _, err := fmtSscan(q.Get("X-Amz-Expires"), &exp); err != nil || time.Since(ts) > time.Duration(exp)*time.Second {
			return "expired"
		}
		q.Del("X-Amz-Signature")
	} else {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
			return "missing Authorization"
		}
		for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ") {
			k, v, _ := strings.Cut(part, "=")
			switch k {
			case "Credential":
				credential = v
			case "SignedHeaders":
				signedHeaders = v
			case "Signature":
				signature = v
			}
		}
		date = r.Header.Get("X-Amz-Date")
		payloadHash = r.Header.Get("X-Amz-Content-Sha256")
		sum := sha256.Sum256(body)
		if payloadHash != hex.EncodeToString(sum[:]) {
			return "payload hash mismatch"
		}
		if !strings.Contains(";"+signedHeaders+";", ";x-amz-date;") || !strings.Contains(";"+signedHeaders+";", ";host;") {
			return "host and x-amz-date must be signed"
		}
	}
	parts := strings.Split(credential, "/")
	if len(parts) != 5 || parts[0] != f.AccessKeyID || parts[2] != f.Region || parts[3] != "s3" || parts[4] != "aws4_request" {
		return "bad credential scope " + credential
	}
	var ch strings.Builder
	for _, h := range strings.Split(signedHeaders, ";") {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		ch.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		qs = append(qs, vEncode(k, false)+"="+vEncode(q.Get(k), false))
	}
	creq := r.Method + "\n" + vEncode(r.URL.Path, true) + "\n" + strings.Join(qs, "&") + "\n" + ch.String() + "\n" + signedHeaders + "\n" + payloadHash
	h := sha256.Sum256([]byte(creq))
	sts := "AWS4-HMAC-SHA256\n" + date + "\n" + strings.Join(parts[1:], "/") + "\n" + hex.EncodeToString(h[:])
	k := vHMAC([]byte("AWS4"+f.Secret), parts[1])
	k = vHMAC(k, parts[2])
	k = vHMAC(k, parts[3])
	k = vHMAC(k, "aws4_request")
	if want := hex.EncodeToString(vHMAC(k, sts)); !hmac.Equal([]byte(want), []byte(signature)) {
		return "signature mismatch"
	}
	return ""
}

func fmtSscan(s string, v *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, io.ErrUnexpectedEOF
		}
		n = n*10 + int(c-'0')
	}
	*v = n
	return 1, nil
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if msg := f.Verify(r, body); msg != "" {
		f.mu.Lock()
		f.Rejects++
		f.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error><Code>SignatureDoesNotMatch</Code><Message>"+msg+"</Message></Error>")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		f.Objects[r.URL.Path] = body
		f.Puts++
	case http.MethodGet:
		b, ok := f.Objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>")
			return
		}
		_, _ = w.Write(b)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
