package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/objstore"
	"github.com/agentshield-ai/agentshield/internal/weft/objstore/s3test"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/google/uuid"
)

// newEnvStore is newEnv that also returns the store and server.
func newEnvStore(t *testing.T, setup func(*store.Store)) (*testEnv, *store.Store, *Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(st)
	}
	s := New(nativeOnly(Config{}), st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	return &testEnv{t: t, srv: hs}, st, s
}

// seedExportProject writes n finished calls, two object versions and a feedback row.
func seedExportProject(e *testEnv, n int) {
	e.t.Helper()
	var batch []any
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("call-%03d", i)
		ts := time.Now().Add(-time.Duration(n-i) * time.Second).UTC().Format(time.RFC3339Nano)
		batch = append(batch, completeSpan(map[string]any{"id": id, "trace_id": id, "name": "answer", "started_at": ts,
			"inputs": map[string]any{"q": i}, "ended_at": ts, "output": map[string]any{"a": i}, "summary": map[string]any{}}))
	}
	e.post("/api/v1/spans/batch", map[string]any{"project_id": pid, "items": batch})
	publish(e, "ds", map[string]any{"rows": []any{1}})
	publish(e, "ds", map[string]any{"rows": []any{1, 2}})
	e.feedback(map[string]any{"ref": spanRef("call-000"), "feedback_type": "weft.reaction", "payload": map[string]any{"emoji": "👍"}})
}

func waitExport(t *testing.T, e *testEnv, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, b := e.raw("/api/v1/exports/"+jobID+"?project_id="+pid, nil, nil)
		if code != 200 {
			t.Fatalf("status -> %d %s", code, b)
		}
		var st map[string]any
		_ = json.Unmarshal(b, &st)
		if st["state"] != "in_progress" {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("export still running: %v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func gunzipLines(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v (%q)", err, b[:min(len(b), 80)])
	}
	var out []map[string]any
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestBulkExportSQLite(t *testing.T) {
	e, st, _ := newEnvStore(t, nil)
	seedExportProject(e, 1203) // more than two export pages
	code, b := e.raw("/api/v1/exports", map[string]any{"project_id": pid, "targets": []any{"calls", "objects", "feedback"}}, nil)
	if code != http.StatusAccepted {
		t.Fatalf("start -> %d %s", code, b)
	}
	var started map[string]any
	_ = json.Unmarshal(b, &started)
	jobID, _ := started["job_id"].(string)
	if _, err := uuid.Parse(jobID); err != nil || started["name"] != "entities/acme/projects/evals/exports/"+jobID || started["created_at"] == nil {
		t.Fatalf("started = %v", started)
	}
	res := waitExport(t, e, jobID)
	if res["state"] != "succeeded" || res["failure"] != nil || res["updated_at"] == nil {
		t.Fatalf("status = %v", res)
	}
	arts := res["artifacts"].([]any)
	if len(arts) != 3 {
		t.Fatalf("artifacts = %v", arts)
	}
	want := map[string]int{"calls": 1203, "objects": 2, "feedback": 1}
	for _, a := range arts {
		am := a.(map[string]any)
		target := am["target"].(string)
		if int(am["expected_rows"].(float64)) != want[target] || am["written_rows"] != am["expected_rows"] {
			t.Fatalf("%s artifact = %v", target, am)
		}
		uri := am["uri"].(string)
		if !strings.HasPrefix(uri, e.srv.URL+"/api/v1/exports/"+jobID+"/artifacts/") || !strings.HasSuffix(uri, "?project_id=acme%2Fevals") {
			t.Fatalf("uri = %s", uri)
		}
		code, body := httpGet(t, uri)
		if code != 200 {
			t.Fatalf("download %s -> %d", uri, code)
		}
		rows := gunzipLines(t, body)
		if len(rows) != want[target] {
			t.Fatalf("%s rows = %d", target, len(rows))
		}
		switch target {
		case "calls":
			r := rows[0]
			if r["id"] != "call-000" || r["project_id"] != pid || r["output"] == nil || r["storage_size_bytes"] == nil {
				t.Fatalf("call row = %v", r)
			}
		case "objects":
			if rows[0]["object_id"] != "ds" || rows[1]["val"] == nil {
				t.Fatalf("object rows = %v", rows)
			}
		case "feedback":
			if rows[0]["feedback_type"] != "weft.reaction" {
				t.Fatalf("feedback row = %v", rows[0])
			}
		}
	}
	// One job at a time per server.
	busy := &store.ExportJob{JobID: uuid.NewString(), ProjectID: pid, Targets: []string{"calls"}}
	if err := st.CreateExportJob(t.Context(), busy); err != nil {
		t.Fatal(err)
	}
	code, b = e.raw("/api/v1/exports", map[string]any{"project_id": pid, "targets": []any{"calls"}}, nil)
	if code != http.StatusConflict || !strings.Contains(string(b), `"code":"export_in_progress"`) {
		t.Fatalf("second export -> %d %s", code, b)
	}
	// A restart fails jobs left in progress.
	if err := st.FailInterruptedExportJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	res = waitExport(t, e, busy.JobID)
	if res["state"] != "failed" || res["failure"].(map[string]any)["code"] != "interrupted" || len(res["artifacts"].([]any)) != 0 {
		t.Fatalf("interrupted = %v", res)
	}
}

func TestBulkExportValidation(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		body any
		code int
	}{
		{map[string]any{"project_id": pid}, 422},
		{map[string]any{"project_id": pid, "targets": []any{}}, 422},
		{map[string]any{"project_id": pid, "targets": []any{"runs"}}, 422},
		{map[string]any{"project_id": pid, "targets": []any{"calls"}, "format": "csv"}, 422},
		{map[string]any{"project_id": pid, "targets": []any{"calls", "objects", "feedback", "calls"}}, 422},
		{map[string]any{"project_id": pid, "targets": []any{"calls", "calls"}}, 400},
		{map[string]any{"targets": []any{"calls"}}, 400}, // no project
	} {
		code, b := e.raw("/api/v1/exports", c.body, nil)
		if code != c.code {
			t.Fatalf("%v -> %d %s, want %d", c.body, code, b, c.code)
		}
		if code == 422 && !strings.Contains(string(b), `"loc"`) {
			t.Fatalf("422 body = %s", b)
		}
	}
	if code, b := e.raw("/api/v1/exports/not-a-uuid?project_id="+pid, nil, nil); code != 422 {
		t.Fatalf("bad job id -> %d %s", code, b)
	}
	code, b := e.raw("/api/v1/exports/"+uuid.NewString()+"?project_id="+pid, nil, nil)
	if code != 404 || !strings.Contains(string(b), `"detail":{"code":"not_found"`) {
		t.Fatalf("unknown job -> %d %s", code, b)
	}
}

func TestBulkExportS3AndRBAC(t *testing.T) {
	fake, s3srv := s3test.NewFake(t, "AKID", "SECRET", "us-east-1")
	e, _, _ := newEnvStore(t, func(st *store.Store) {
		b, err := objstore.New(objstore.Config{Bucket: "byob", Prefix: "weft", Endpoint: s3srv.URL,
			Creds: objstore.Credentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET"}})
		if err != nil {
			t.Fatal(err)
		}
		st.SetFileBackend(b)
	})
	seedExportProject(e, 3)
	code, b := e.raw("/api/v1/exports", map[string]any{"project_id": pid, "targets": []any{"calls"}}, nil)
	if code != 202 {
		t.Fatalf("start -> %d %s", code, b)
	}
	var started map[string]any
	_ = json.Unmarshal(b, &started)
	jobID := started["job_id"].(string)
	res := waitExport(t, e, jobID)
	art := res["artifacts"].([]any)[0].(map[string]any)
	uri := art["uri"].(string)
	if uri != "s3://byob/weft/"+pid+"/exports/"+jobID+"/calls.jsonl.gz" || art["written_rows"].(float64) != 3 {
		t.Fatalf("artifact = %v", art)
	}
	if fake.Objects["/byob/weft/"+pid+"/exports/"+jobID+"/calls.jsonl.gz"] == nil {
		t.Fatal("artifact not uploaded")
	}
	// Downloadable through /api/v1/files/resolve (presigned) ...
	r := e.post("/api/v1/files/resolve", map[string]any{"project_id": pid, "uris": []any{uri}})
	code, body := httpGet(t, r["download_urls"].([]any)[0].(string))
	if code != 200 || len(gunzipLines(t, body)) != 3 {
		t.Fatalf("presigned download -> %d", code)
	}
	// ... and through the download route, which redirects to S3.
	code, body = httpGet(t, e.srv.URL+"/api/v1/exports/"+jobID+"/artifacts/calls?project_id="+pid)
	if code != 200 || len(gunzipLines(t, body)) != 3 {
		t.Fatalf("download route -> %d", code)
	}

	// RBAC: team admins export, members and viewers may not.
	f := newRBAC(t)
	f.e.mustCode(200, "POST", "/api/v1/spans/start", f.bob, callStart("acme/app", "c1"))
	exp := map[string]any{"project_id": "acme/app", "targets": []any{"calls"}}
	f.e.mustCode(http.StatusForbidden, "POST", "/api/v1/exports", f.bob, exp)
	f.e.mustCode(http.StatusForbidden, "POST", "/api/v1/exports", f.dave, exp)
	out := f.e.mustCode(http.StatusAccepted, "POST", "/api/v1/exports", f.alice, exp)
	var st map[string]any
	_ = json.Unmarshal(out, &st)
	f.e.mustCode(http.StatusForbidden, "GET", "/api/v1/exports/"+st["job_id"].(string)+"?project_id=acme/app", f.carol, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw := f.e.mustCode(200, "GET", "/api/v1/exports/"+st["job_id"].(string)+"?project_id=acme/app", f.alice, nil)
		if !bytes.Contains(raw, []byte(`"in_progress"`)) {
			if !bytes.Contains(raw, []byte(`"succeeded"`)) {
				t.Fatalf("rbac export = %s", raw)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rbac export did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
