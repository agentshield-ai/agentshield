# Platform Support

This document describes the platforms and agent integrations supported by AgentShield, including current coverage gaps and guidance for contributing platform-specific detection rules.

## Supported Platforms

| Platform | Status | Notes |
|----------|--------|-------|
| Linux    | Primary target | Server-side agent deployments |
| macOS    | Supported | Development and local agent use |
| Windows  | Engine runs, no detection rules | See [Windows Coverage](#windows-coverage) |

Detection rules assume **Unix/POSIX command semantics** (e.g. `bash`, `chmod`, `sudo`, `cat`, `nc`, `mkfifo`). The macOS keychain (`security find-generic-password`) is also covered. All existing rules use `logsource.product: ai_agent` with `category: agent_events`.

## Agent Platform Integrations

| Integration | Language | Status |
|-------------|----------|--------|
| [OpenClaw](plugins/openclaw/) | TypeScript | Production-ready |
| [Claude Code](plugins/claude/) | Bash hooks | Planned |
| Generic HTTP API | Any | Available via `POST /api/v1/evaluate` |

Any platform capable of making HTTP requests can integrate with AgentShield through the evaluation API. See the [API Reference](docs/api.md) for details.

## Windows Coverage

Detection rules **do not currently cover Windows-specific commands**, including but not limited to:

- PowerShell (`powershell.exe`, `pwsh`)
- Command Prompt (`cmd.exe`)
- Windows utilities (`certutil`, `reg.exe`, `wmic`, `bitsadmin`, `mshta`)
- Windows credential stores (Credential Manager, DPAPI)
- Windows-specific persistence mechanisms (Registry run keys, Scheduled Tasks)

### Why not Windows?

Server-side AI agent deployments are predominantly Linux. Local development agents typically run on macOS. Windows coverage has not been prioritised because the primary threat surface -- cloud-hosted agents executing tool calls -- is overwhelmingly Unix-based.

The Go engine binary **does compile and run on Windows** (no CGO dependency), so the platform gap is purely in the detection rule corpus, not the engine itself.

### Contributing Windows Rules

Windows-specific rules are welcome. To contribute:

1. Follow the existing Sigma rule format under `rules/rules/ai_agent/`
2. Use `logsource.product: ai_agent` with `category: agent_events` (consistent with existing rules)
3. Add a `logsource.product: windows` tag if the rule is Windows-specific
4. Name the file with the `ai_agent_` prefix (e.g. `ai_agent_windows_powershell_download.yml`)
5. Include MITRE ATT&CK references where applicable
6. Submit a pull request to the [sigma-ai](https://github.com/agentshield-ai/sigma-ai) upstream repository

## Architecture Note

AgentShield's HTTP API model means **any platform** -- including Windows -- can send evaluation requests to the engine. Platform-specific detection logic lives entirely in the Sigma rule corpus, not in the engine code. Adding Windows coverage requires new rules, not code changes.

```
Windows Agent  ──┐
Linux Agent    ──┼── POST /api/v1/evaluate ──> Engine ──> Sigma Rules
macOS Agent    ──┘
```

The engine evaluates events against all loaded rules regardless of the client platform. To detect Windows-specific threats, the rule corpus simply needs Windows-aware patterns.
