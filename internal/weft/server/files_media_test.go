package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/objstore"
	"github.com/agentshield-ai/agentshield/internal/weft/objstore/s3test"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// newEnvWithStore is newEnv with a hook to configure the store first.
func newEnvWithStore(t *testing.T, setup func(*store.Store)) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(st)
	}
	s := New(Config{}, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	return &testEnv{t: t, srv: hs}
}

func (e *testEnv) uploadFile(content []byte) string {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("project_id", pid)
	fw, _ := mw.CreateFormFile("file", "f.bin")
	_, _ = fw.Write(content)
	_ = mw.Close()
	resp, err := http.Post(e.srv.URL+"/file/create", mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		e.t.Fatalf("file/create -> %d %v", resp.StatusCode, out)
	}
	return out["digest"].(string)
}

func httpGet(t *testing.T, u string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestFilesResolveURLSQLite(t *testing.T) {
	e := newEnv(t)
	d := e.uploadFile([]byte("hello sqlite"))
	res := e.post("/files/resolve_url", map[string]any{"project_id": pid, "uris": []any{d, "weft-file://" + d, "https://example.com/x.png"}})
	urls := res["download_urls"].([]any)
	if len(urls) != 3 || res["expires_at"] != nil || urls[2] != "https://example.com/x.png" {
		t.Fatalf("resolve = %v", res)
	}
	u := urls[0].(string)
	if !strings.HasPrefix(u, e.srv.URL+"/file/content?") || urls[1] != u {
		t.Fatalf("expected a Weft /file/content URL, got %v", urls)
	}
	if code, body := httpGet(t, u); code != 200 || string(body) != "hello sqlite" {
		t.Fatalf("GET %s -> %d %s", u, code, body)
	}
	for _, bad := range []any{"nope", "s3://bucket/key", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if code, out := e.raw("/files/resolve_url", map[string]any{"project_id": pid, "uris": []any{bad}}, nil); code != 400 {
			t.Fatalf("resolve %v -> %d %s", bad, code, out)
		}
	}
}

func TestFilesS3Backend(t *testing.T) {
	fake, s3srv := s3test.NewFake(t, "AKWEFT", "s3cr3t", "us-west-2")
	var backend *objstore.S3
	e := newEnvWithStore(t, func(st *store.Store) {
		b, err := objstore.New(objstore.Config{Bucket: "byob", Prefix: "weft", Endpoint: s3srv.URL, Region: "us-west-2",
			Creds: objstore.Credentials{AccessKeyID: "AKWEFT", SecretAccessKey: "s3cr3t"}})
		if err != nil {
			t.Fatal(err)
		}
		backend = b
		st.SetFileBackend(b)
	})
	content := []byte("\x89PNG\r\n\x1a\n fake image bytes")
	d := e.uploadFile(content)
	key := "/byob/weft/" + pid + "/" + d
	if !bytes.Equal(fake.Objects[key], content) || fake.Puts != 1 || fake.Rejects != 0 {
		t.Fatalf("object not written to S3 (puts=%d rejects=%d keys=%v)", fake.Puts, fake.Rejects, fake.Requests)
	}
	// Uploading the same content again does not re-upload.
	e.uploadFile(content)
	if fake.Puts != 1 {
		t.Fatalf("duplicate upload re-sent: puts=%d", fake.Puts)
	}
	// Reads go through the backend.
	code, body := httpGet(t, e.srv.URL+"/file/content?project_id="+pid+"&digest="+d)
	if code != 200 || !bytes.Equal(body, content) {
		t.Fatalf("file/content -> %d %q", code, body)
	}
	stats := e.post("/files/query_stats", map[string]any{"project_id": pid})
	if stats["total_size_bytes"].(float64) != float64(len(content)) {
		t.Fatalf("files stats = %v", stats)
	}
	// resolve_url returns presigned URLs that the (verifying) fake accepts.
	res := e.post("/files/resolve_url", map[string]any{"project_id": pid, "uris": []any{d, backend.URI("weft/" + pid + "/" + d)}, "expires_in": 600})
	urls := res["download_urls"].([]any)
	if res["expires_at"] == nil || len(urls) != 2 {
		t.Fatalf("resolve = %v", res)
	}
	for _, u := range urls {
		if !strings.Contains(u.(string), "X-Amz-Signature=") {
			t.Fatalf("not presigned: %v", u)
		}
		if code, body := httpGet(t, u.(string)); code != 200 || !bytes.Equal(body, content) {
			t.Fatalf("presigned GET -> %d %q", code, body)
		}
	}
	// Keys of other projects or buckets are refused.
	for _, bad := range []string{"s3://byob/weft/other/proj/" + d, "s3://elsewhere/weft/" + pid + "/" + d} {
		if code, out := e.raw("/files/resolve_url", map[string]any{"project_id": pid, "uris": []any{bad}}, nil); code != 400 {
			t.Fatalf("resolve %s -> %d %s", bad, code, out)
		}
	}
	// SQLite holds metadata only.
	if fake.Rejects != 0 {
		t.Fatalf("fake S3 rejected %d requests", fake.Rejects)
	}
}

func TestImageCreateMock(t *testing.T) {
	e := newEnv(t)
	res := e.post("/image/create", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "mock/image", "prompt": "a red fox", "n": 2}})
	resp := res["response"].(map[string]any)
	data := resp["data"].([]any)
	if len(data) != 2 || res["call_id"] == nil {
		t.Fatalf("image/create = %v", res)
	}
	png, err := base64.StdEncoding.DecodeString(data[0].(map[string]any)["b64_json"].(string))
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("not a PNG: %v", err)
	}
	call := e.post("/call/read", map[string]any{"project_id": pid, "id": res["call_id"]})["call"].(map[string]any)
	if !strings.Contains(call["op_name"].(string), "weft.images.generate") || call["inputs"].(map[string]any)["prompt"] != "a red fox" {
		t.Fatalf("image call = %v", call)
	}
	untracked := e.post("/image/create", map[string]any{"project_id": pid, "track_llm_call": false,
		"inputs": map[string]any{"model": "mock/image", "prompt": "x"}})
	if untracked["call_id"] != nil || len(untracked["response"].(map[string]any)["data"].([]any)) != 1 {
		t.Fatalf("untracked = %v", untracked)
	}
	// Provider errors come back in the response, like /completions/create.
	errRes := e.post("/image/create", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "anthropic/claude-x", "prompt": "x"}})
	if errRes["response"].(map[string]any)["error"] == nil {
		t.Fatalf("expected error response: %v", errRes)
	}
	if code, _ := e.raw("/image/create", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "mock/image"}}, nil); code != 400 {
		t.Fatalf("missing prompt -> %d", code)
	}
}

func TestImageCreateOpenAICompatible(t *testing.T) {
	e := newEnv(t)
	var got map[string]any
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer img-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://img.example/1.png"}],"usage":{"input_tokens":4,"output_tokens":100,"total_tokens":104}}`)
	}))
	defer img.Close()
	e.post("/secrets/set", map[string]any{"name": "IMG_KEY", "value": "img-key"})
	publish(e, "imgprov", map[string]any{"_type": "Provider", "_class_name": "Provider", "base_url": img.URL + "/v1", "api_key_name": "IMG_KEY"})
	res := e.post("/image/create", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "imgprov/gpt-image-1", "prompt": "a cat", "size": "256x256"}})
	if res["response"].(map[string]any)["data"] == nil || got["model"] != "gpt-image-1" || got["size"] != "256x256" {
		t.Fatalf("image via provider = %v (sent %v)", res, got)
	}
	call := e.post("/call/read", map[string]any{"project_id": pid, "id": res["call_id"]})["call"].(map[string]any)
	usage := call["summary"].(map[string]any)["usage"].(map[string]any)
	if u, ok := usage["imgprov/gpt-image-1"].(map[string]any); !ok || u["total_tokens"].(float64) != 104 || u["images"].(float64) != 1 {
		t.Fatalf("image usage = %v", usage)
	}
}
