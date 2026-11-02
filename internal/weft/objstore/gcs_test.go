package objstore

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/objstore/gcstest"
	"github.com/agentshield-ai/agentshield/internal/weft/objstore/s3test"
)

func TestGCSSignedURL(t *testing.T) {
	fake, hs := gcstest.NewFake(t)
	fake.Objects["/media-bkt/frames/a b+1.png"] = []byte("PNGDATA")
	g, err := NewGCS(GCSConfig{Bucket: "media-bkt", ServiceAccountJSON: fake.ServiceAccountJSON(), Endpoint: hs.URL})
	if err != nil {
		t.Fatal(err)
	}
	u, exp, err := g.PresignGet("frames/a b+1.png", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(exp) < 9*time.Minute || !strings.Contains(u, "X-Goog-Signature=") || !strings.Contains(u, "X-Goog-Expires=600") {
		t.Fatalf("url %s exp %v", u, exp)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "PNGDATA" {
		t.Fatalf("status %d body %q", resp.StatusCode, b)
	}
	// Tampering with the path or expiry breaks the signature.
	bad := strings.Replace(u, "X-Goog-Expires=600", "X-Goog-Expires=6000", 1)
	if r, _ := http.Get(bad); r.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered url accepted: %d", r.StatusCode)
	}
	if got, err := g.Get(context.Background(), "frames/a b+1.png"); err != nil || string(got) != "PNGDATA" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if _, err := g.Get(context.Background(), "missing.png"); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, _, err := g.PresignGet("x", 8*24*time.Hour); err == nil {
		t.Fatal("ttl over 7 days accepted")
	}
}

func TestGCSServiceAccountErrors(t *testing.T) {
	for _, raw := range []string{"not json", `{"client_email":"a@b"}`, `{"client_email":"a@b","private_key":"nope"}`} {
		if _, err := NewGCS(GCSConfig{Bucket: "b", ServiceAccountJSON: []byte(raw)}); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestS3PutReader(t *testing.T) {
	fake, hs := s3test.NewFake(t, "AKID", "SECRET", "us-east-1")
	s, err := New(Config{Bucket: "bkt", Endpoint: hs.URL, Creds: Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "up")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	content := strings.Repeat("line\n", 10000)
	_, _ = f.WriteString(content)
	_, _ = f.Seek(0, io.SeekStart)
	if err := s.PutReader(context.Background(), "exports/x.jsonl", f, int64(len(content)), "application/jsonl"); err != nil {
		t.Fatal(err)
	}
	if string(fake.Objects["/bkt/exports/x.jsonl"]) != content {
		t.Fatal("content mismatch")
	}
}
