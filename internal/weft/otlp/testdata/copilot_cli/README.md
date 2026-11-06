# Copilot CLI fixtures: derived from documentation, not captured

These exports were written by hand from GitHub's documentation of Copilot
CLI's OpenTelemetry export (docs.github.com, "GitHub Copilot CLI command
reference", section "OpenTelemetry monitoring"): the span kinds, the
attributes listed for `invoke_agent`, `chat` and `execute_tool`, the span
events (`github.copilot.hook.*`, `exception` with `github.copilot.error_*`),
the resource (`service.name` `github-copilot`) and the scope
(`github.copilot`). Span names follow the GenAI semantic conventions
(`chat <model>`, `execute_tool <tool>`), which the documentation says the
CLI follows but does not spell out. Copilot CLI was not installed or run.

Ids, times, token counts, the version and the message text are invented.

- `from_docs_content_on.json`: `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true`.
  Turn 1 runs `bash` (ok), `view` (fails) and `task` (an `explore`
  sub-agent with its own model call), then answers. Turn 2 fails: its model
  call carries `error.type` with the span status unset, and the invocation
  ends with an `exception` event.
- `from_docs_content_off.json`: the same spans without the content
  attributes (the default).
