package objstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/agentshield-ai/agentshield/internal/weft/objstore/s3test"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// AWS documentation examples (Signature Version 4, S3).
var awsExampleCreds = Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}

func awsExampleTime() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }

func TestPresignMatchesAWSExample(t *testing.T) {
	ps := false
	s, err := New(Config{Bucket: "examplebucket", Region: "us-east-1", Creds: awsExampleCreds, PathStyle: &ps, Now: awsExampleTime})
	if err != nil {
		t.Fatal(err)
	}
	got, exp, err := s.PresignGet("test.txt", 86400*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Fatalf("presigned URL\n got %s\nwant %s", got, want)
	}
	if !exp.Equal(awsExampleTime().Add(24 * time.Hour)) {
		t.Fatalf("expiry = %v", exp)
	}
}

func TestHeaderSignatureMatchesAWSExample(t *testing.T) {
	ps := false
	s, err := New(Config{Bucket: "examplebucket", Region: "us-east-1", Creds: awsExampleCreds, PathStyle: &ps, Now: awsExampleTime})
	if err != nil {
		t.Fatal(err)
	}
	// "GET Object" example: GET /test.txt with Range: bytes=0-9.
	req, _ := http.NewRequest(http.MethodGet, s.objectURL("test.txt").String(), nil)
	req.Header.Set("Range", "bytes=0-9")
	// The AWS example signs range too; sign it the same way by treating it as an x-amz-style header.
	sig := signForTest(t, s, req, []string{"host", "range", "x-amz-content-sha256", "x-amz-date"})
	if want := "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"; sig != want {
		t.Fatalf("signature = %s, want %s", sig, want)
	}
}

// signForTest computes the signature of req over the given headers with the
// client's own signing primitives (used for the AWS vector that signs Range).
func signForTest(t *testing.T, s *S3, req *http.Request, names []string) string {
	t.Helper()
	now := s.cfg.Now().UTC()
	sum := sha256.Sum256(nil)
	req.Header.Set("X-Amz-Date", now.Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
	var ch strings.Builder
	for _, n := range names {
		v := req.Header.Get(n)
		if n == "host" {
			v = req.URL.Host
		}
		ch.WriteString(n + ":" + v + "\n")
	}
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), "", ch.String(), strings.Join(names, ";"), hex.EncodeToString(sum[:])}, "\n")
	return s.signature(now, canonical)
}

func TestS3RoundTripAgainstVerifyingFake(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKTEST", SecretAccessKey: "secret/key+with=chars", SessionToken: "tok"}
	fake, hs := s3test.NewFake(t, creds.AccessKeyID, creds.SecretAccessKey, "eu-west-2")
	bucket, prefix, err := ParseURL("s3://weft-files/team/prod/")
	if err != nil || bucket != "weft-files" || prefix != "team/prod" {
		t.Fatalf("ParseURL = %q %q %v", bucket, prefix, err)
	}
	s, err := New(Config{Bucket: bucket, Prefix: prefix, Endpoint: hs.URL, Region: "eu-west-2", Creds: creds})
	if err != nil {
		t.Fatal(err)
	}
	key := s.Key("acme/app/abc DEF+1")
	if key != "team/prod/acme/app/abc DEF+1" {
		t.Fatalf("key = %q", key)
	}
	ctx := context.Background()
	if err := s.Put(ctx, key, []byte("hello s3")); err != nil {
		t.Fatal(err)
	}
	if fake.Objects["/weft-files/team/prod/acme/app/abc DEF+1"] == nil {
		t.Fatalf("object not stored path-style: %v", fake.Objects)
	}
	b, err := s.Get(ctx, key)
	if err != nil || string(b) != "hello s3" {
		t.Fatalf("Get = %q %v", b, err)
	}
	if _, err := s.Get(ctx, s.Key("missing")); err != ErrNotFound {
		t.Fatalf("missing object err = %v", err)
	}
	u, _, err := s.PresignGet(key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello s3" {
		t.Fatalf("presigned GET = %d %s", resp.StatusCode, body)
	}
	// Tampering with a presigned URL or using the wrong secret is rejected.
	pu, _ := url.Parse(u)
	q := pu.Query()
	q.Set("X-Amz-Expires", "999")
	pu.RawQuery = q.Encode()
	if resp, _ := http.Get(pu.String()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered presigned URL status = %d", resp.StatusCode)
	}
	bad, _ := New(Config{Bucket: bucket, Endpoint: hs.URL, Region: "eu-west-2", Creds: Credentials{AccessKeyID: "AKTEST", SecretAccessKey: "wrong"}})
	if err := bad.Put(ctx, "x", []byte("y")); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("wrong secret err = %v", err)
	}
	if fake.Rejects != 2 || fake.Puts != 1 {
		t.Fatalf("rejects=%d puts=%d", fake.Rejects, fake.Puts)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{Bucket: "b"}); err == nil {
		t.Fatal("missing credentials should fail")
	}
	if _, _, err := ParseURL("https://bucket/x"); err == nil {
		t.Fatal("non-s3 URL should fail")
	}
	s, err := New(Config{Bucket: "b", Region: "eu-central-1", Creds: awsExampleCreds})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.objectURL("k/1").String(); got != "https://b.s3.eu-central-1.amazonaws.com/k/1" {
		t.Fatalf("AWS virtual-hosted URL = %s", got)
	}
}
