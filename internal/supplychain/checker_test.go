package supplychain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- ExtractPackages tests ---

func TestExtractPackages_npm_install(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     map[string]interface{}
		want     []PackageRef
	}{
		{
			name:     "npm install single package",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "npm install express"},
			want:     []PackageRef{{Name: "express", Registry: RegistryNPM}},
		},
		{
			name:     "npm install multiple packages",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "npm install express lodash axios"},
			want: []PackageRef{
				{Name: "express", Registry: RegistryNPM},
				{Name: "lodash", Registry: RegistryNPM},
				{Name: "axios", Registry: RegistryNPM},
			},
		},
		{
			name:     "npm i shorthand",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "npm i react"},
			want:     []PackageRef{{Name: "react", Registry: RegistryNPM}},
		},
		{
			name:     "npm install with save-dev flag",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "npm install --save-dev vitest"},
			want:     []PackageRef{{Name: "vitest", Registry: RegistryNPM}},
		},
		{
			name:     "npm install with version specifier",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "npm install express@4.18.0"},
			want:     []PackageRef{{Name: "express", Registry: RegistryNPM}},
		},
		{
			name:     "npm add alias",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "npm add chalk"},
			want:     []PackageRef{{Name: "chalk", Registry: RegistryNPM}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractPackages(tt.toolName, tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractPackages() returned %d refs, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i].Name != tt.want[i].Name || got[i].Registry != tt.want[i].Registry {
					t.Errorf("ref[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractPackages_pip_install(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     map[string]interface{}
		want     []PackageRef
	}{
		{
			name:     "pip install single package",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "pip install requests"},
			want:     []PackageRef{{Name: "requests", Registry: RegistryPyPI}},
		},
		{
			name:     "pip3 install",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "pip3 install flask"},
			want:     []PackageRef{{Name: "flask", Registry: RegistryPyPI}},
		},
		{
			name:     "pip install with version",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "pip install requests>=2.28"},
			want:     []PackageRef{{Name: "requests", Registry: RegistryPyPI}},
		},
		{
			name:     "pip install multiple packages",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "pip install requests flask gunicorn"},
			want: []PackageRef{
				{Name: "requests", Registry: RegistryPyPI},
				{Name: "flask", Registry: RegistryPyPI},
				{Name: "gunicorn", Registry: RegistryPyPI},
			},
		},
		{
			name:     "pip install with -r flag skips requirements file arg",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "pip install -r requirements.txt flask"},
			want:     []PackageRef{{Name: "flask", Registry: RegistryPyPI}},
		},
		{
			name:     "pip install with upgrade flag",
			toolName: "Bash",
			args:     map[string]interface{}{"command": "pip install --upgrade pip"},
			want:     []PackageRef{{Name: "pip", Registry: RegistryPyPI}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractPackages(tt.toolName, tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractPackages() returned %d refs, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i].Name != tt.want[i].Name || got[i].Registry != tt.want[i].Registry {
					t.Errorf("ref[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractPackages_file_write(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     map[string]interface{}
		wantLen  int
	}{
		{
			name:     "package.json with dependencies",
			toolName: "Write",
			args: map[string]interface{}{
				"file_path": "/tmp/project/package.json",
				"content":   `{"dependencies":{"express":"^4.18.0","lodash":"^4.17.21"},"devDependencies":{"vitest":"^1.0.0"}}`,
			},
			wantLen: 3,
		},
		{
			name:     "requirements.txt",
			toolName: "Write",
			args: map[string]interface{}{
				"file_path": "/tmp/project/requirements.txt",
				"content":   "requests>=2.28\nflask==2.3.0\n# comment\ngunicorn",
			},
			wantLen: 3,
		},
		{
			name:     "unrelated file write is ignored",
			toolName: "Write",
			args: map[string]interface{}{
				"file_path": "/tmp/project/README.md",
				"content":   "# Hello",
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractPackages(tt.toolName, tt.args)
			if len(got) != tt.wantLen {
				t.Errorf("ExtractPackages() returned %d refs, want %d: %v", len(got), tt.wantLen, got)
			}
		})
	}
}

func TestExtractPackages_no_command_returns_nil(t *testing.T) {
	got := ExtractPackages("Bash", map[string]interface{}{})
	if got != nil {
		t.Errorf("expected nil for empty args, got %v", got)
	}
}

func TestExtractPackages_deduplicates(t *testing.T) {
	args := map[string]interface{}{
		"command": "npm install express express lodash",
	}
	got := ExtractPackages("Bash", args)
	if len(got) != 2 {
		t.Errorf("expected 2 deduplicated refs, got %d: %v", len(got), got)
	}
}

// --- CheckPackage tests ---

func TestCheckPackage_npm_clean(t *testing.T) {
	oldDate := time.Now().AddDate(-1, 0, 0).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := npmRegistryResponse{
			Name: "express",
			Time: npmTimeResponse{Created: oldDate},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	result, err := checker.CheckPackage(context.Background(), "express", RegistryNPM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictClean {
		t.Errorf("verdict = %s, want %s", result.Verdict, VerdictClean)
	}
}

func TestCheckPackage_npm_not_found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	result, err := checker.CheckPackage(context.Background(), "nonexistent-pkg-xyz", RegistryNPM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictNotFound {
		t.Errorf("verdict = %s, want %s", result.Verdict, VerdictNotFound)
	}
}

func TestCheckPackage_npm_suspicious_age(t *testing.T) {
	recentDate := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := npmRegistryResponse{
			Name: "new-pkg",
			Time: npmTimeResponse{Created: recentDate},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	result, err := checker.CheckPackage(context.Background(), "new-pkg", RegistryNPM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictSuspiciousAge {
		t.Errorf("verdict = %s, want %s", result.Verdict, VerdictSuspiciousAge)
	}
}

func TestCheckPackage_pypi_clean(t *testing.T) {
	oldDate := time.Now().AddDate(-1, 0, 0).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := pypiResponse{
			Info: pypiInfo{Name: "requests", Version: "2.31.0"},
			URLs: []pypiReleaseFile{{UploadTimeISO: oldDate}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	result, err := checker.CheckPackage(context.Background(), "requests", RegistryPyPI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictClean {
		t.Errorf("verdict = %s, want %s", result.Verdict, VerdictClean)
	}
}

func TestCheckPackage_pypi_not_found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	result, err := checker.CheckPackage(context.Background(), "nonexistent-pypi-pkg", RegistryPyPI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictNotFound {
		t.Errorf("verdict = %s, want %s", result.Verdict, VerdictNotFound)
	}
}

func TestCheckPackage_timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	// Use a context with a very short deadline
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := checker.CheckPackage(ctx, "slow-pkg", RegistryNPM)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestCheckPackage_malformed_json(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	result, err := checker.CheckPackage(context.Background(), "bad-json-pkg", RegistryNPM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictUnknown {
		t.Errorf("verdict = %s, want %s for malformed JSON", result.Verdict, VerdictUnknown)
	}
}

func TestCheckPackage_unsupported_registry(t *testing.T) {
	checker := NewPackageChecker(CheckerConfig{MinAgeDays: 7, TimeoutSec: 5})
	result, err := checker.CheckPackage(context.Background(), "some-pkg", Registry("cargo"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictUnknown {
		t.Errorf("verdict = %s, want %s for unsupported registry", result.Verdict, VerdictUnknown)
	}
}

func TestCheckPackage_server_error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	result, err := checker.CheckPackage(context.Background(), "error-pkg", RegistryNPM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictUnknown {
		t.Errorf("verdict = %s, want %s for server error", result.Verdict, VerdictUnknown)
	}
}

func TestCheckPackages_cancellation(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	checker := newTestChecker(srv.URL, 7)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	refs := []PackageRef{
		{Name: "pkg1", Registry: RegistryNPM},
		{Name: "pkg2", Registry: RegistryNPM},
	}
	results := checker.CheckPackages(ctx, refs)
	// Should stop early due to cancellation
	if len(results) > 1 {
		t.Errorf("expected at most 1 result due to cancellation, got %d", len(results))
	}
}

// --- Typosquat detection tests ---

func TestCheckTyposquat_detects_npm_typosquat(t *testing.T) {
	tests := []struct {
		name      string
		pkg       string
		registry  Registry
		wantTypo  bool
		wantMatch string
	}{
		{
			name:      "expresss (extra s) detected as typosquat of express",
			pkg:       "expresss",
			registry:  RegistryNPM,
			wantTypo:  true,
			wantMatch: "express",
		},
		{
			name:      "axio (deletion) detected as typosquat of axios",
			pkg:       "axio",
			registry:  RegistryNPM,
			wantTypo:  true,
			wantMatch: "axios",
		},
		{
			name:      "lodasg (substitution) detected as typosquat of lodash",
			pkg:       "lodasg",
			registry:  RegistryNPM,
			wantTypo:  true,
			wantMatch: "lodash",
		},
		{
			name:     "express (exact match) is NOT a typosquat",
			pkg:      "express",
			registry: RegistryNPM,
			wantTypo: false,
		},
		{
			name:     "totally-different-name is NOT a typosquat",
			pkg:      "totally-different-name",
			registry: RegistryNPM,
			wantTypo: false,
		},
		{
			name:     "short names (3 chars) are not checked",
			pkg:      "axo",
			registry: RegistryNPM,
			wantTypo: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckTyposquat(tt.pkg, tt.registry)
			if result.IsTyposquat != tt.wantTypo {
				t.Errorf("CheckTyposquat(%q) IsTyposquat = %v, want %v", tt.pkg, result.IsTyposquat, tt.wantTypo)
			}
			if tt.wantTypo && result.SimilarTo != tt.wantMatch {
				t.Errorf("CheckTyposquat(%q) SimilarTo = %q, want %q", tt.pkg, result.SimilarTo, tt.wantMatch)
			}
		})
	}
}

func TestCheckTyposquat_detects_pypi_typosquat(t *testing.T) {
	tests := []struct {
		name      string
		pkg       string
		wantTypo  bool
		wantMatch string
	}{
		{
			name:      "reqeusts (transposition) detected",
			pkg:       "reqeusts",
			wantTypo:  true,
			wantMatch: "requests",
		},
		{
			name:      "flaask (insertion) detected",
			pkg:       "flaask",
			wantTypo:  true,
			wantMatch: "flask",
		},
		{
			name:     "requests (exact) is not a typosquat",
			pkg:      "requests",
			wantTypo: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckTyposquat(tt.pkg, RegistryPyPI)
			if result.IsTyposquat != tt.wantTypo {
				t.Errorf("CheckTyposquat(%q) IsTyposquat = %v, want %v", tt.pkg, result.IsTyposquat, tt.wantTypo)
			}
			if tt.wantTypo && result.SimilarTo != tt.wantMatch {
				t.Errorf("CheckTyposquat(%q) SimilarTo = %q, want %q", tt.pkg, result.SimilarTo, tt.wantMatch)
			}
		})
	}
}

func TestCheckTyposquat_unsupported_registry(t *testing.T) {
	result := CheckTyposquat("express", Registry("cargo"))
	if result.IsTyposquat {
		t.Error("expected no typosquat detection for unsupported registry")
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"express", "expresss", 1},
		{"axios", "axois", 2},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			got := levenshtein(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// --- Test helpers ---

// newTestChecker creates a PackageChecker that routes requests to a local
// httptest server instead of the real registries. It does this by overriding
// the checker's http.Client transport to rewrite URLs.
func newTestChecker(baseURL string, minAgeDays int) *PackageChecker {
	checker := NewPackageChecker(CheckerConfig{
		MinAgeDays:     minAgeDays,
		TimeoutSec:     5,
		CheckDownloads: false,
	})
	// Replace the client with one that rewrites URLs to our test server
	checker.client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &rewriteTransport{
			base:    http.DefaultTransport,
			testURL: baseURL,
		},
	}
	return checker
}

// rewriteTransport rewrites request URLs to point at a test server.
type rewriteTransport struct {
	base    http.RoundTripper
	testURL string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL scheme+host to our test server, keeping the path
	req.URL.Scheme = "http"
	req.URL.Host = rt.testURL[len("http://"):]
	return rt.base.RoundTrip(req)
}
