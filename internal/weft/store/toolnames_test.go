package store

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestToolNamesMatch(t *testing.T) {
	tests := []struct {
		source, verdict, span string
		want                  bool
	}{
		// Same name, any case, any harness.
		{"", "Bash", "Bash", true},
		{"", "bash", "Bash", true},
		{"openclaw", "exec", "exec", true},
		{"", "", "Bash", false},
		{"", "Bash", "", false},
		// Gemini CLI: the connector's canonical names against Gemini's span names.
		{"gemini", "exec", "run_shell_command", true},
		{"gemini", "read", "read_file", true},
		{"gemini", "read", "read_many_files", true},
		{"gemini", "write", "write_file", true},
		{"gemini", "edit", "replace", true},
		{"gemini", "browser", "web_fetch", true},
		{"gemini", "web_search", "google_web_search", true},
		{"gemini", "planning", "write_todos", true},
		{"gemini", "memory", "save_memory", true},
		{"gemini", "glob", "glob", true}, // unmapped: passes through
		{"gemini", "search_file_content", "search_file_content", true},
		{"gemini", "list_directory", "list_directory", true},
		{"gemini", "exec", "read_file", false},
		{"gemini", "read", "write_file", false},
		{"gemini", "grep", "glob", false},
		{"gemini", "run_shell_command", "run_shell_command", true}, // a verdict naming the tool as Gemini does
		{"gemini-cli", "exec", "run_shell_command", true},
		// Codex: the hook's Bash → exec, Codex's telemetry names exec_command / shell.
		{"codex", "exec", "exec_command", true},
		{"codex", "exec", "shell", true},
		{"codex", "exec", "local_shell", true},
		{"codex", "Bash", "exec_command", true},
		{"codex", "edit", "apply_patch", true},
		{"codex", "sessions_spawn", "spawn_agent", true},
		{"codex", "exec", "write_stdin", false},
		{"codex", "exec", "apply_patch", false},
		// OpenCode.
		{"opencode", "exec", "bash", true},
		{"opencode", "read", "read", true},
		{"opencode", "edit", "multiedit", true},
		{"opencode", "browser", "webfetch", true},
		{"opencode", "sessions_spawn", "task", true},
		{"opencode", "exec", "read", false},
		// Copilot CLI.
		{"copilot", "exec", "bash", true},
		{"github-copilot", "read", "view", true},
		{"copilot", "write", "create", true},
		{"copilot", "browser", "web_fetch", true},
		{"copilot", "exec", "read_bash", false},
		{"copilot", "read", "bash", false},
		// Claude Code (event_source claude_code).
		{"claude_code", "exec", "Bash", true},
		{"claude_code", "read", "Read", true},
		{"claude_code", "edit", "MultiEdit", true},
		{"claude_code", "sessions_spawn", "Task", true},
		{"claude_code", "exec", "Read", false},
		// The harness is the verdict's: a Copilot verdict "read" is not Gemini's read_file.
		{"copilot", "read", "read_file", false},
		// Unknown harness: any table may match, a different tool never does.
		{"", "exec", "run_shell_command", true},
		{"", "exec", "exec_command", true},
		{"", "exec", "bash", true},
		{"hermes", "read", "read_file", true},
		{"", "exec", "read_file", false},
		{"", "write", "edit", false},
	}
	for _, tc := range tests {
		if got := ToolNamesMatch(tc.source, tc.verdict, tc.span); got != tc.want {
			t.Errorf("ToolNamesMatch(%q, %q, %q) = %v, want %v", tc.source, tc.verdict, tc.span, got, tc.want)
		}
	}
}

// No name means two different tools: in every table that has it (ignoring
// case), a name maps to the same canonical name, so an unknown harness may
// try every table.
func TestToolAliasTablesConsistent(t *testing.T) {
	seen := map[string]string{}
	where := map[string]string{}
	for h, tab := range harnessToolAliases {
		lower := map[string]string{}
		for k, v := range tab {
			lk, lv := asciiLower(k), asciiLower(v)
			if prev, ok := lower[lk]; ok && prev != lv {
				t.Errorf("%s: %q maps to %q and %q", h, lk, prev, lv)
			}
			lower[lk] = lv
			if prev, ok := seen[lk]; ok && prev != lv {
				t.Errorf("%q is %q in %s but %q in %s", lk, prev, where[lk], lv, h)
			}
			seen[lk], where[lk] = lv, h
		}
	}
	for src, h := range harnessBySource {
		if _, ok := harnessToolAliases[h]; !ok {
			t.Errorf("source %q names harness %q without a table", src, h)
		}
	}
}

// The Go tables mirror the AgentShield connectors' tables.
func TestToolAliasTablesMatchConnectors(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	pyEntry := regexp.MustCompile(`^\s*"([^"]+)":\s*"([^"]+)",`)
	parsePy := func(t *testing.T, rel string) map[string]string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Skipf("connector not present: %v", err)
		}
		_, body, ok := strings.Cut(string(b), "_TOOL_ALIASES")
		if !ok {
			t.Fatalf("%s: no _TOOL_ALIASES", rel)
		}
		body, _, _ = strings.Cut(body, "\n}")
		out := map[string]string{}
		for _, line := range strings.Split(body, "\n") {
			if m := pyEntry.FindStringSubmatch(line); m != nil {
				out[m[1]] = m[2]
			}
		}
		if len(out) < 10 {
			t.Fatalf("%s: parsed only %d entries", rel, len(out))
		}
		return out
	}
	shCase := regexp.MustCompile(`^\s*([A-Za-z0-9_|]+)\) printf '([a-z_]+)' ;;`)
	parseSh := func(t *testing.T, rel string) map[string]string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Skipf("connector not present: %v", err)
		}
		_, body, ok := strings.Cut(string(b), "as_normalise_name() {")
		if !ok {
			t.Fatalf("%s: no as_normalise_name", rel)
		}
		body, _, _ = strings.Cut(body, "esac")
		out := map[string]string{}
		for _, line := range strings.Split(body, "\n") {
			if m := shCase.FindStringSubmatch(line); m != nil {
				for _, n := range strings.Split(m[1], "|") {
					out[n] = m[2]
				}
			}
		}
		if len(out) < 10 {
			t.Fatalf("%s: parsed only %d entries", rel, len(out))
		}
		return out
	}
	tests := []struct {
		harness, file string
		parse         func(*testing.T, string) map[string]string
	}{
		{harnessGemini, "plugins/gemini/agentshield_gemini/normalise.py", parsePy},
		{harnessCodex, "plugins/codex/normalise.py", parsePy},
		{harnessClaudeCode, "plugins/claude/agentshield-lib.sh", parseSh},
	}
	for _, tc := range tests {
		t.Run(tc.harness, func(t *testing.T) {
			want := tc.parse(t, tc.file)
			got := harnessToolAliases[tc.harness]
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s maps %q to %q, the Go table to %q", tc.file, k, v, got[k])
				}
			}
		})
	}
}

// A verdict without a tool call id, naming the tool by AgentShield's
// canonical name, links to the harness's tool span by session and time,
// whichever arrives first.
func TestCorrelationHarnessToolAliases(t *testing.T) {
	tests := []struct {
		name, source, verdictTool string
		spans                     []detTestSpan // under root "root", trace t1, session s1
		at                        time.Duration
		want                      string // span id, "" for none
	}{
		{
			name: "gemini exec → run_shell_command", source: "gemini", verdictTool: "exec",
			spans: []detTestSpan{
				toolSpan("read", "t1", "root", "", "read_file", "", "gemini-cli", 1000*time.Millisecond),
				toolSpan("shell", "t1", "root", "", "run_shell_command", "", "gemini-cli", 1200*time.Millisecond),
			},
			at: 1010 * time.Millisecond, want: "shell", // read_file is nearer but another tool
		},
		{
			name: "gemini edit → replace", source: "gemini", verdictTool: "edit",
			spans: []detTestSpan{toolSpan("replace", "t1", "root", "", "replace", "", "gemini-cli", time.Second)},
			at:    time.Second, want: "replace",
		},
		{
			name: "gemini exec does not take read_file", source: "gemini", verdictTool: "exec",
			spans: []detTestSpan{toolSpan("read", "t1", "root", "", "read_file", "", "gemini-cli", time.Second)},
			at:    time.Second,
		},
		{
			name: "codex exec → exec_command", source: "codex", verdictTool: "exec",
			spans: []detTestSpan{
				toolSpan("stdin", "t1", "root", "", "write_stdin", "", "codex", 1000*time.Millisecond),
				toolSpan("cmd", "t1", "root", "", "exec_command", "", "codex", 1500*time.Millisecond),
			},
			at: 1000 * time.Millisecond, want: "cmd",
		},
		{
			name: "codex edit → apply_patch", source: "codex", verdictTool: "edit",
			spans: []detTestSpan{toolSpan("patch", "t1", "root", "", "apply_patch", "", "codex", time.Second)},
			at:    time.Second, want: "patch",
		},
		{
			name: "opencode exec → bash", source: "opencode", verdictTool: "exec",
			spans: []detTestSpan{toolSpan("bash", "t1", "root", "", "bash", "", "opencode", time.Second)},
			at:    time.Second, want: "bash",
		},
		{
			name: "copilot read → view", source: "copilot", verdictTool: "read",
			spans: []detTestSpan{
				toolSpan("bash", "t1", "root", "", "bash", "", "copilot", 1000*time.Millisecond),
				toolSpan("view", "t1", "root", "", "view", "", "copilot", 1300*time.Millisecond),
			},
			at: 1000 * time.Millisecond, want: "view",
		},
		{
			name: "claude code exec → Bash", source: "claude_code", verdictTool: "exec",
			spans: []detTestSpan{toolSpan("bash", "t1", "root", "", "Bash", "", "coder", time.Second)},
			at:    time.Second, want: "bash",
		},
	}
	for _, tc := range tests {
		for _, detFirst := range []bool{false, true} {
			name := tc.name + " (spans first)"
			if detFirst {
				name = tc.name + " (verdict first)"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t)
				d := det("evt", "s1", tc.verdictTool, "", "block", tc.at)
				d.EventSource = tc.source
				spans := append([]detTestSpan{agentRoot("root", "t1", "s1", "agent", 0)}, tc.spans...)
				if detFirst {
					mustUpsert(t, s, d)
					putSpans(t, s, spans...)
				} else {
					putSpans(t, s, spans...)
					mustUpsert(t, s, d)
				}
				got := getDet(t, s, "evt")
				if spanOf(got) != tc.want {
					t.Fatalf("linked to %q via %q, want %q", spanOf(got), got.Link, tc.want)
				}
				if tc.want != "" && got.Link != LinkNearestTime {
					t.Fatalf("link = %q", got.Link)
				}
			})
		}
	}
}
