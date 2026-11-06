package otlp

// OpenAI Codex CLI's trace export ([otel] trace_exporter in config.toml) is
// not mapped: Weft drops every span of a resource whose service.name starts
// with "codex" (codex_exec, codex_cli_rs, codex-tui, ...). Codex sessions
// come from its log events instead (codex_logs.go).
//
// Codex traces its own Rust functions through tracing-opentelemetry: about
// 1,500 spans for two turns (fs.get_metadata, append_items,
// persist_rollout_items, auth, plugins_for_config, ...), with dozens of
// roots per export. None carries a session id Weft reads (Codex's
// conversation.id is on a few spans only, thread.id is a worker-thread
// number), so they would be stored as session-less noise roots. The only
// GenAI-convention attributes in captured exports (Codex 0.167.0) are
// gen_ai.usage.* token counts on handle_responses, without a model name;
// the generic mapping would bill them to model "unknown" in those orphan
// traces, a second time, since the log events' response.completed already
// carries the same tokens on the session's chat spans. So nothing is kept.

import "strings"

// isCodexResource reports a resource exported by Codex CLI (any of its
// binaries: codex_exec, codex_cli_rs, ...).
func isCodexResource(resource map[string]any) bool {
	return strings.HasPrefix(aStr(resource, "service.name"), "codex")
}
