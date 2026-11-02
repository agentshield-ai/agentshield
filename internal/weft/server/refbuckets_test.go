package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/objstore/gcstest"
	"github.com/agentshield-ai/agentshield/internal/weft/objstore/s3test"
)

func TestReferenceBucketsS3AndGCS(t *testing.T) {
	s3fake, s3srv := s3test.NewFake(t, "CUSTKEY", "CUSTSECRET", "eu-west-1")
	s3fake.Objects["/cust-media/frames/000123.png"] = []byte("S3PNG")
	s3fake.Objects["/cust-media/private/x.png"] = []byte("NO")
	gfake, gsrv := gcstest.NewFake(t)
	gfake.Objects["/cust-gcs/shots/cat.png"] = []byte("GCSPNG")

	e := newEnv(t)
	e.post("/secrets/set", map[string]any{"name": "CUST_S3", "value": `{"access_key_id":"CUSTKEY","secret_access_key":"CUSTSECRET"}`})
	e.post("/secrets/set", map[string]any{"name": "CUST_GCS", "value": string(gfake.ServiceAccountJSON())})
	e.post("/secrets/set", map[string]any{"name": "WRONG_S3", "value": `{"access_key_id":"CUSTKEY","secret_access_key":"nope"}`})

	// Test connection reports failures without saving.
	res := e.post("/reference_buckets/test", map[string]any{"entity": "acme", "provider": "s3", "bucket": "cust-media", "region": "eu-west-1",
		"endpoint": s3srv.URL, "secret_name": "WRONG_S3", "test_object_key": "frames/000123.png"})
	if res["ok"] != false || !strings.Contains(res["error"].(string), "403") {
		t.Fatalf("bad creds test = %v", res)
	}
	res = e.post("/reference_buckets/test", map[string]any{"entity": "acme", "provider": "s3", "bucket": "cust-media", "region": "eu-west-1",
		"endpoint": s3srv.URL, "secret_name": "CUST_S3", "test_object_key": "frames/missing.png"})
	if res["ok"] != false || !strings.Contains(res["error"].(string), "not found") {
		t.Fatalf("missing object test = %v", res)
	}
	// Register an S3 bucket for team acme (limited to frames/) and a GCS bucket for one project.
	code, b := e.raw("/reference_buckets/create", map[string]any{"entity": "acme", "provider": "s3", "bucket": "s3://cust-media/",
		"prefix": "frames/", "region": "eu-west-1", "endpoint": s3srv.URL, "secret_name": "WRONG_S3", "test_object_key": "frames/000123.png"}, nil)
	if code != 400 {
		t.Fatalf("create with failing test -> %d %s", code, b)
	}
	s3b := e.post("/reference_buckets/create", map[string]any{"entity": "acme", "provider": "aws", "bucket": "s3://cust-media/",
		"prefix": "frames/", "region": "eu-west-1", "endpoint": s3srv.URL, "secret_name": "CUST_S3", "test_object_key": "frames/000123.png"})["bucket"].(map[string]any)
	if s3b["bucket"] != "cust-media" || s3b["provider"] != "s3" || s3b["scope"] != "entity" || s3b["verified_at"] == nil {
		t.Fatalf("s3 bucket = %v", s3b)
	}
	e.post("/reference_buckets/create", map[string]any{"project_id": pid, "provider": "gcs", "bucket": "cust-gcs",
		"endpoint": gsrv.URL, "secret_name": "CUST_GCS", "test_object_key": "shots/cat.png"})
	if code, b := e.raw("/reference_buckets/create", map[string]any{"entity": "acme", "provider": "s3", "bucket": "cust-media",
		"prefix": "frames/", "region": "eu-west-1", "endpoint": s3srv.URL, "secret_name": "CUST_S3", "skip_test": true}, nil); code != 400 {
		t.Fatalf("duplicate -> %d %s", code, b)
	}

	// resolve_url presigns both, for the project.
	res = e.post("/files/resolve_url", map[string]any{"project_id": pid, "uris": []any{
		"s3://cust-media/frames/000123.png", "gs://cust-gcs/shots/cat.png", "https://example.com/a.png"}})
	urls := res["download_urls"].([]any)
	if len(urls) != 3 || res["expires_at"] == nil || urls[2] != "https://example.com/a.png" {
		t.Fatalf("resolve = %v", res)
	}
	if !strings.Contains(urls[0].(string), "X-Amz-Signature=") || !strings.Contains(urls[1].(string), "X-Goog-Signature=") {
		t.Fatalf("urls = %v", urls)
	}
	for i, want := range []string{"S3PNG", "GCSPNG"} {
		if code, body := httpGet(t, urls[i].(string)); code != 200 || string(body) != want {
			t.Fatalf("GET %s -> %d %q", urls[i], code, body)
		}
	}
	// Outside the prefix, in another team's project, or for an unregistered bucket: refused.
	for _, c := range []struct{ project, uri string }{
		{pid, "s3://cust-media/private/x.png"},
		{"other/proj", "s3://cust-media/frames/000123.png"},
		{"acme/other", "gs://cust-gcs/shots/cat.png"},
		{pid, "gs://unknown-bucket/a.png"},
	} {
		if code, b := e.raw("/files/resolve_url", map[string]any{"project_id": c.project, "uris": []any{c.uri}}, nil); code != 400 ||
			!strings.Contains(string(b), "not registered") {
			t.Fatalf("%s in %s -> %d %s", c.uri, c.project, code, b)
		}
	}
	// The team bucket also serves the team's other projects.
	res = e.post("/files/resolve_url", map[string]any{"project_id": "acme/other", "uris": []any{"s3://cust-media/frames/000123.png"}})
	if code, _ := httpGet(t, res["download_urls"].([]any)[0].(string)); code != 200 {
		t.Fatalf("team bucket from acme/other -> %d", code)
	}

	// Listing.
	list := e.post("/reference_buckets/list", map[string]any{"project_id": pid})["buckets"].([]any)
	if len(list) != 2 {
		t.Fatalf("project list = %v", list)
	}
	code, b = e.raw("/reference_buckets/list?entity=acme", nil, nil)
	var lr map[string]any
	_ = json.Unmarshal(b, &lr)
	if code != 200 || len(lr["buckets"].([]any)) != 2 {
		t.Fatalf("entity list -> %d %s", code, b)
	}
	if strings.Contains(string(b), "CUSTSECRET") || strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatal("list leaks credentials")
	}
	// Delete.
	e.post("/reference_buckets/delete", map[string]any{"id": s3b["id"]})
	if code, _ := e.raw("/files/resolve_url", map[string]any{"project_id": pid, "uris": []any{"s3://cust-media/frames/000123.png"}}, nil); code != 400 {
		t.Fatalf("after delete -> %d", code)
	}
	if code, _ := e.raw("/reference_buckets/delete", map[string]any{"id": s3b["id"]}, nil); code != 404 {
		t.Fatalf("delete twice -> %d", code)
	}
	// Validation.
	for _, body := range []map[string]any{
		{"entity": "acme", "provider": "azure", "bucket": "x1x", "skip_test": true},
		{"entity": "acme", "provider": "s3", "bucket": "Bad_Bucket!", "skip_test": true},
		{"provider": "s3", "bucket": "okbucket", "skip_test": true},
		{"entity": "acme", "provider": "gcs", "bucket": "okbucket", "skip_test": true},
		{"entity": "acme", "provider": "gcs", "bucket": "okbucket", "secret_name": "MISSING", "skip_test": true},
	} {
		if code, b := e.raw("/reference_buckets/create", body, nil); code != 400 {
			t.Fatalf("%v -> %d %s", body, code, b)
		}
	}
}

func TestReferenceBucketsNeedServerAdmin(t *testing.T) {
	f := newRBAC(t)
	body := map[string]any{"entity": "acme", "provider": "s3", "bucket": "cust-media", "skip_test": true, "project_id": "acme/app"}
	f.e.mustCode(http.StatusForbidden, "POST", "/reference_buckets/create", f.alice, body)
	f.e.mustCode(http.StatusForbidden, "POST", "/reference_buckets/create", f.bob, body)
	f.e.mustCode(http.StatusForbidden, "POST", "/reference_buckets/list", f.bob, map[string]any{"entity": "acme"})
	f.e.mustCode(http.StatusOK, "POST", "/reference_buckets/list", f.carol, map[string]any{"project_id": "acme/app"})
	f.e.mustCode(http.StatusForbidden, "POST", "/reference_buckets/list", f.dave, map[string]any{"project_id": "acme/app"})
}
