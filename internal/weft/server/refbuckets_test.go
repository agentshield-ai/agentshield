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
	// CORS rules for the Weft origin (here the test server's), as Weave's
	// byob-references page configures them.
	s3fake.SetCORS("cust-media", s3test.CORSRule{AllowedOrigins: []string{e.srv.URL}, AllowedMethods: []string{"GET", "HEAD"},
		AllowedHeaders: []string{"*"}, ExposeHeaders: []string{"ETag"}, MaxAgeSeconds: 3000})
	gfake.SetCORS("cust-gcs", gcstest.CORSEntry{Origin: []string{e.srv.URL}, Method: []string{"GET", "HEAD"}, ResponseHeader: []string{"ETag"}, MaxAgeSeconds: 3000})
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
	// per_uri_errors: one request, failures reported per URI, the rest resolved.
	res = e.post("/files/resolve_url", map[string]any{"project_id": pid, "per_uri_errors": true, "uris": []any{
		"s3://cust-media/frames/000123.png", "gs://unknown-bucket/a.png", "s3://cust-media/private/x.png", "https://example.com/a.png",
		"nope", "s3://", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}})
	urls = res["download_urls"].([]any)
	errs := res["errors"].([]any)
	if len(urls) != 7 || len(errs) != 7 || res["expires_at"] == nil {
		t.Fatalf("per-URI resolve = %v", res)
	}
	if !strings.Contains(urls[0].(string), "X-Amz-Signature=") || errs[0] != nil || urls[3] != "https://example.com/a.png" || errs[3] != nil {
		t.Fatalf("per-URI successes = %v", res)
	}
	for i, code := range map[int]string{1: "bucket_not_registered", 2: "bucket_not_registered", 4: "invalid_uri", 5: "invalid_uri", 6: "file_not_found"} {
		e, _ := errs[i].(map[string]any)
		if urls[i] != "" || e == nil || e["code"] != code || e["message"] == "" {
			t.Fatalf("uri %d: url %v error %v, want %s", i, urls[i], errs[i], code)
		}
	}
	// Without it the response keeps Weave's shape (no "errors").
	if res = e.post("/files/resolve_url", map[string]any{"project_id": pid, "uris": []any{"https://example.com/a.png"}}); len(res) != 2 {
		t.Fatalf("plain resolve = %v", res)
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

// The bucket test checks CORS for the Weft origin (the request's Origin, else
// this server's own scheme://host), as Weave's does, and create refuses a
// bucket whose CORS rules do not allow it unless skip_test.
func TestReferenceBucketCORSCheck(t *testing.T) {
	s3fake, s3srv := s3test.NewFake(t, "CUSTKEY", "CUSTSECRET", "eu-west-1")
	s3fake.Objects["/cust-media/frames/1.png"] = []byte("S3PNG")
	gfake, gsrv := gcstest.NewFake(t)
	gfake.Objects["/cust-gcs/shots/cat.png"] = []byte("GCSPNG")
	e := newEnv(t)
	e.post("/secrets/set", map[string]any{"name": "CUST_S3", "value": `{"access_key_id":"CUSTKEY","secret_access_key":"CUSTSECRET"}`})
	e.post("/secrets/set", map[string]any{"name": "CUST_GCS", "value": string(gfake.ServiceAccountJSON())})
	s3body := map[string]any{"entity": "acme", "provider": "s3", "bucket": "cust-media", "region": "eu-west-1",
		"endpoint": s3srv.URL, "secret_name": "CUST_S3", "test_object_key": "frames/1.png"}
	gcsBody := map[string]any{"project_id": pid, "provider": "gcs", "bucket": "cust-gcs", "endpoint": gsrv.URL,
		"secret_name": "CUST_GCS", "test_object_key": "shots/cat.png"}
	test := func(body map[string]any, origin string) map[string]any {
		t.Helper()
		var h map[string]string
		if origin != "" {
			h = map[string]string{"Origin": origin}
		}
		code, b := e.raw("/reference_buckets/test", body, h)
		var m map[string]any
		if code != 200 || json.Unmarshal(b, &m) != nil {
			t.Fatalf("test -> %d %s", code, b)
		}
		return m
	}
	check := func(m map[string]any, part string) map[string]any {
		t.Helper()
		c, _ := m[part].(map[string]any)
		if c == nil {
			t.Fatalf("no %s in %v", part, m)
		}
		return c
	}

	// No CORS configuration: S3 refuses the preflight (403); the read itself works.
	m := test(s3body, "")
	if m["ok"] != false || check(m, "read")["ok"] != true || check(m, "cors")["ok"] != false ||
		check(m, "cors")["origin"] != e.srv.URL || !strings.Contains(m["error"].(string), "CORS is not enabled for this bucket") {
		t.Fatalf("no CORS = %v", m)
	}
	if code, b := e.raw("/reference_buckets/create", s3body, nil); code != 400 || !strings.Contains(string(b), "CORS") {
		t.Fatalf("create without CORS -> %d %s", code, b)
	}
	// Rules for another origin: refused, naming the origin tested.
	s3fake.SetCORS("cust-media", s3test.CORSRule{AllowedOrigins: []string{"https://weft.example.com"}, AllowedMethods: []string{"GET", "HEAD"}})
	m = test(s3body, "")
	if m["ok"] != false || !strings.Contains(m["error"].(string), "This CORS request is not allowed") || !strings.Contains(m["error"].(string), e.srv.URL) {
		t.Fatalf("other origin = %v", m)
	}
	// The request's Origin (the UI's origin) is the one tested.
	m = test(s3body, "https://weft.example.com")
	if m["ok"] != true || m["error"] != nil || check(m, "cors")["origin"] != "https://weft.example.com" || check(m, "cors")["error"] != nil {
		t.Fatalf("matching origin = %v", m)
	}
	// HEAD only: the preflight for GET is refused.
	s3fake.SetCORS("cust-media", s3test.CORSRule{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"HEAD"}})
	if m = test(s3body, ""); m["ok"] != false || check(m, "cors")["ok"] != false {
		t.Fatalf("HEAD only = %v", m)
	}
	// A wildcard rule passes, and create stores the bucket as verified.
	s3fake.SetCORS("cust-media", s3test.CORSRule{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET", "HEAD"}})
	if m = test(s3body, ""); m["ok"] != true {
		t.Fatalf("wildcard = %v", m)
	}
	if b := e.post("/reference_buckets/create", s3body)["bucket"].(map[string]any); b["verified_at"] == nil {
		t.Fatalf("created = %v", b)
	}
	// Missing object and CORS failure are reported together.
	s3fake.SetCORS("cust-media")
	missing := map[string]any{}
	for k, v := range s3body {
		missing[k] = v
	}
	missing["test_object_key"] = "frames/none.png"
	m = test(missing, "")
	if m["ok"] != false || check(m, "read")["ok"] != false || check(m, "cors")["ok"] != false ||
		!strings.Contains(m["error"].(string), "not found") || !strings.Contains(m["error"].(string), "CORS") {
		t.Fatalf("missing + no CORS = %v", m)
	}

	// GCS answers a refused preflight with 200 and no CORS headers.
	m = test(gcsBody, "")
	if m["ok"] != false || check(m, "read")["ok"] != true || !strings.Contains(m["error"].(string), "no Access-Control-Allow-Origin") {
		t.Fatalf("gcs no CORS = %v", m)
	}
	gfake.SetCORS("cust-gcs", gcstest.CORSEntry{Origin: []string{"https://weft.example.com"}, Method: []string{"GET", "HEAD"}})
	if m = test(gcsBody, "https://weft.example.com"); m["ok"] != true {
		t.Fatalf("gcs matching origin = %v", m)
	}
	if m = test(gcsBody, "http://evil.test"); m["ok"] != false {
		t.Fatalf("gcs other origin = %v", m)
	}
	// Behind a proxy, the forwarded scheme and host are the origin.
	code, b := e.raw("/reference_buckets/test", gcsBody, map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "weft.example.com"})
	if code != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("forwarded origin -> %d %s", code, b)
	}
	// An unreachable bucket has no CORS result.
	unreachable := map[string]any{}
	for k, v := range gcsBody {
		unreachable[k] = v
	}
	unreachable["endpoint"] = "http://127.0.0.1:1"
	if m = test(unreachable, ""); m["ok"] != false || m["cors"] != nil || !strings.Contains(m["error"].(string), "cannot reach") {
		t.Fatalf("unreachable = %v", m)
	}
}
