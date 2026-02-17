# AgentShield: Implementation Plans for Identified Gaps

Each plan below addresses one of the 10 critical gaps identified in the codebase review. Plans are ordered by impact -- the most architecturally significant changes come first.

---

## Plan 1: Pre-Execution Hook System (Reactive -> Proactive)

### Problem
AgentShield only detects threats *after* commands execute. By the time a notification arrives, the damage is done. The current architecture polls log files every 5 seconds (`daemon.py:91`) and then adds LLM triage latency on top. This is a monitor, not a shield.

### Goal
Intercept commands *before* execution, apply Sigma rules synchronously, and block dangerous commands with sub-100ms latency. No LLM in the hot path.

### Design

```
Agent (e.g. Claude Code)
  │
  ├── Hook: PreToolUse  ──→  AgentShield Hook Script
  │                              │
  │                              ├── Read command from stdin (JSON)
  │                              ├── POST to local Unix socket
  │                              │     └── /var/run/agentshield.sock
  │                              ├── AgentShield Gate evaluates:
  │                              │     1. Sigma rules (compiled, cached)
  │                              │     2. Local allowlist/blocklist
  │                              │     3. Baseline patterns (from DB)
  │                              ├── Response: allow / block / ask
  │                              └── Exit code: 0=allow, 2=block
  │
  ├── Tool executes (if allowed)
  │
  └── Hook: PostToolUse ──→  AgentShield logs result for async triage
```

### Implementation

#### 1.1 New module: `src/agentshield/gate/`

```
gate/
├── __init__.py
├── server.py      # Unix domain socket server (asyncio)
├── evaluator.py   # Synchronous rule evaluation (no LLM)
├── allowlist.py   # User-managed allow/blocklist
└── hook.py        # Shell hook script generator
```

**`server.py`** -- Lightweight async server on a Unix domain socket.
- Accepts JSON: `{"tool_name": "Bash", "command": "curl ... | bash", "working_dir": "/tmp"}`
- Returns JSON: `{"decision": "block", "rule_id": "agent-rce-injection-001", "reason": "..."}`
- Timeout: 50ms max. If evaluation exceeds this, default to allow (fail-open for usability).
- Keeps DetectionEngine and allowlist in memory -- no DB queries in the hot path.

**`evaluator.py`** -- Reuses `DetectionEngine` but adds:
- Allowlist check first (O(1) hash lookup -- skip rule evaluation for known-safe commands).
- Blocklist check (permanent blocks for patterns like `rm -rf /`).
- Only runs Sigma rules if not in allow/blocklist.
- Returns a `GateDecision` enum: `ALLOW`, `BLOCK`, `ASK` (for MEDIUM-level matches).

**`allowlist.py`** -- Simple YAML-backed allow/blocklist:
```yaml
# ~/.agentshield/allowlist.yaml
allow:
  - pattern: "git *"
    reason: "Standard git operations"
  - pattern: "npm install"
    reason: "Package management"
block:
  - pattern: "* | bash"
    reason: "Pipe to shell execution"
  - pattern: "rm -rf /"
    reason: "Filesystem destruction"
```
- Loaded once at startup, hot-reloaded on SIGHUP.
- Patterns use fnmatch-style glob matching.

**`hook.py`** -- Generates hook configuration for Claude Code:
```python
def generate_claude_code_hook() -> dict:
    """Generate .claude/hooks.json entry for AgentShield."""
    return {
        "hooks": {
            "PreToolUse": [{
                "matcher": "Bash|Write|Edit",
                "command": "agentshield gate-check"
            }]
        }
    }
```

#### 1.2 New CLI command: `agentshield gate-check`

- Reads tool invocation JSON from stdin.
- Sends to Unix socket server.
- Prints block reason to stderr if blocked.
- Exit code 0 = allow, 2 = block.
- If socket not available, fails open (exit 0) with a warning.

#### 1.3 New CLI command: `agentshield gate start`

- Starts the gate server on the Unix socket.
- Runs in the same process as the existing daemon (share DetectionEngine).
- Add `--gate` flag to `agentshield start -f` to enable gating alongside monitoring.

#### 1.4 Integration with existing daemon

In `MonitorDaemon.__init__`:
- Add optional `gate_enabled: bool = False` parameter.
- If enabled, start the gate server as an asyncio task alongside the polling loop.
- The gate server and the polling daemon share the same `DetectionEngine` instance (rules loaded once).

#### 1.5 Files to modify
- `daemon.py` -- Add gate server lifecycle management.
- `cli.py` -- Add `gate-check` and `gate start` commands.
- `config.py` -- Add `gate_enabled: bool`, `gate_socket_path: Path`, `gate_timeout_ms: int`.

#### 1.6 What NOT to do
- No LLM calls in the gate path. Ever. Latency budget is 50ms.
- No database queries in the gate path. Everything in memory.
- No network calls. Pure local evaluation.

---

## Plan 2: Local-First Triage (Reduce LLM Dependency)

### Problem
Every alert triggers an Anthropic API call with extended thinking (`triage/agent.py:175-185`). This is expensive, slow, requires internet, and sends all agent commands to a third party. Without an API key, triage is completely disabled (`daemon.py:148-149`) and everything falls back to SUSPICIOUS.

### Goal
Handle 90%+ of triage decisions locally. Reserve LLM for genuinely ambiguous cases.

### Design

```
Alert comes in
  │
  ├── Stage 1: Deterministic checks (< 1ms)
  │     ├── Allowlist match? → FALSE_POSITIVE (auto)
  │     ├── Blocklist match? → TRUE_POSITIVE (auto)
  │     └── Exact baseline match? → FALSE_POSITIVE (auto)
  │
  ├── Stage 2: Heuristic scoring (< 5ms)
  │     ├── Rule FP rate from feedback history
  │     ├── Command similarity to known FPs (fuzzy match)
  │     ├── Working directory risk (system dirs vs project dirs)
  │     ├── Time-of-day anomaly (unusual hours)
  │     └── Aggregate → confidence score
  │
  ├── Stage 3: Decision
  │     ├── Score > 0.85 → auto-decide locally
  │     ├── Score 0.4-0.85 → escalate to LLM (if available)
  │     └── Score < 0.4 → auto-decide locally
  │
  └── Stage 4: LLM triage (only for ambiguous cases)
```

### Implementation

#### 2.1 New module: `src/agentshield/triage/local.py`

```python
class LocalTriageScorer:
    """Fast local triage without LLM dependency."""

    def __init__(
        self,
        feedback_store: FeedbackStore,
        alert_store: AlertStore,
        allowlist: Allowlist | None = None,
    ) -> None: ...

    async def score(self, alert: Alert, context: AlertContext) -> LocalScore:
        """Score an alert locally. Returns verdict + confidence."""
        ...

class LocalScore(BaseModel):
    verdict: Verdict
    confidence: float  # 0.0 - 1.0
    reasoning: str
    factors: list[ScoringFactor]
    needs_llm: bool  # True if confidence is in ambiguous range
```

**Scoring factors** (each returns a weighted signal):

| Factor | Weight | Logic |
|--------|--------|-------|
| `allowlist_match` | 1.0 | Exact match → instant FP |
| `blocklist_match` | 1.0 | Exact match → instant TP |
| `baseline_exact` | 0.9 | Same command previously marked safe |
| `baseline_fuzzy` | 0.6 | Similar command (Levenshtein ratio > 0.85) |
| `rule_fp_rate` | 0.7 | Rule's historical FP rate |
| `working_dir_risk` | 0.3 | System path = risky, project path = safer |
| `command_entropy` | 0.2 | Base64/encoded args = suspicious |
| `time_anomaly` | 0.1 | Outside normal working hours |

#### 2.2 Fuzzy command matching

Replace the exact-match-only `_commands_match` in `context.py:225-247`:

```python
def _commands_match(self, cmd1: str | None, cmd2: str | None) -> tuple[bool, float]:
    """Compare commands with fuzzy matching.

    Returns (matches, similarity_score).
    """
    if cmd1 is None or cmd2 is None:
        return False, 0.0

    # Normalize: strip, lowercase, collapse whitespace
    norm1 = " ".join(cmd1.strip().lower().split())
    norm2 = " ".join(cmd2.strip().lower().split())

    # Exact match
    if norm1 == norm2:
        return True, 1.0

    # Extract command name (first token) -- must match
    base1 = norm1.split()[0] if norm1 else ""
    base2 = norm2.split()[0] if norm2 else ""
    if base1 != base2:
        return False, 0.0

    # Levenshtein ratio for arguments
    ratio = _levenshtein_ratio(norm1, norm2)
    return ratio > 0.85, ratio
```

Use a simple Levenshtein implementation (no external dependency -- keep it lightweight):
```python
def _levenshtein_ratio(s1: str, s2: str) -> float:
    """Calculate normalized Levenshtein similarity ratio."""
    if not s1 and not s2:
        return 1.0
    max_len = max(len(s1), len(s2))
    if max_len == 0:
        return 1.0
    distance = _levenshtein_distance(s1, s2)
    return 1.0 - (distance / max_len)
```

#### 2.3 Modify `daemon.py` triage flow

```python
async def _triage_alert(self, alert: Alert) -> Alert:
    context = await self._context_gatherer.gather_context(alert)

    # Stage 1+2: Local scoring
    local_score = await self._local_scorer.score(alert, context)

    if not local_score.needs_llm:
        # Resolved locally -- no API call needed
        return self._apply_verdict(alert, local_score.verdict, local_score.reasoning, context)

    # Stage 3: Escalate to LLM only for ambiguous cases
    if self._triage_agent is not None:
        decision = await self._triage_agent.triage(alert, context)
        return self._apply_verdict(alert, decision.verdict, decision.reasoning, context)

    # No LLM available -- use local score as-is
    return self._apply_verdict(alert, local_score.verdict, local_score.reasoning, context)
```

#### 2.4 Files to modify
- `daemon.py` -- Wire in `LocalTriageScorer` before `TriageAgent`.
- `triage/context.py` -- Upgrade `_commands_match` to fuzzy matching.
- `config.py` -- Add `llm_escalation_threshold: float = 0.85`, `llm_enabled: bool = True`.

#### 2.5 Dependencies
- No new dependencies. Levenshtein implemented in pure Python (< 30 lines).

---

## Plan 3: Expanded Detection Rules

### Problem
Only 5 rules covering a narrow slice of the threat surface. Major attack categories are undetected.

### Goal
Add 8 new rules covering the most critical missing categories, bringing total to 13.

### New Rules

#### 3.1 `agent_data_exfiltration.yml` (CRITICAL)
```yaml
id: agent-data-exfil-001
title: Data Exfiltration via HTTP POST
description: Detects commands that POST file contents to external URLs.
level: critical
detection:
  selection_curl_post:
    event_type: tool_call
    command|contains:
      - 'curl -X POST'
      - 'curl --data'
      - 'curl -d @'
      - 'curl -F file=@'
  selection_wget_post:
    event_type: tool_call
    command|contains:
      - 'wget --post-file'
      - 'wget --post-data'
  selection_curl_upload:
    event_type: tool_call
    command|contains:
      - 'curl -T '
      - 'curl --upload-file'
  condition: selection_curl_post or selection_wget_post or selection_curl_upload
```

#### 3.2 `agent_privilege_escalation.yml` (HIGH)
```yaml
id: agent-privesc-001
title: Privilege Escalation Attempt
description: Detects sudo usage, setuid changes, and capability manipulation.
level: high
detection:
  selection_sudo:
    event_type: tool_call
    command|startswith: 'sudo '
  selection_chmod_suid:
    event_type: tool_call
    command|contains:
      - 'chmod u+s'
      - 'chmod 4'
      - 'chmod +s'
  selection_chown_root:
    event_type: tool_call
    command|contains: 'chown root'
  selection_capabilities:
    event_type: tool_call
    command|contains: 'setcap'
  condition: selection_sudo or selection_chmod_suid or selection_chown_root or selection_capabilities
```

#### 3.3 `agent_system_file_tampering.yml` (CRITICAL)
```yaml
id: agent-sys-tamper-001
title: System File Modification
description: Detects writes to critical system directories and binaries.
level: critical
detection:
  selection_etc_write:
    event_type: tool_call
    command|contains:
      - '> /etc/'
      - 'tee /etc/'
      - 'cp * /etc/'
  selection_bin_write:
    event_type: tool_call
    command|contains:
      - '> /usr/bin/'
      - '> /usr/local/bin/'
      - 'cp * /usr/bin/'
  selection_write_tool_etc:
    event_type: file_write
    file_path|startswith:
      - '/etc/'
      - '/usr/bin/'
      - '/usr/local/bin/'
  condition: selection_etc_write or selection_bin_write or selection_write_tool_etc
```

#### 3.4 `agent_shell_config_modification.yml` (HIGH)
```yaml
id: agent-shell-config-001
title: Shell Configuration Modification
description: Detects writes to shell startup files and SSH authorized_keys.
level: high
detection:
  selection_bashrc:
    event_type: tool_call
    command|contains:
      - '>> ~/.bashrc'
      - '>> ~/.zshrc'
      - '>> ~/.profile'
      - '>> ~/.bash_profile'
  selection_ssh_keys:
    event_type: tool_call
    command|contains:
      - '>> ~/.ssh/authorized_keys'
      - '> ~/.ssh/authorized_keys'
  selection_write_tool:
    event_type: file_write
    file_path|endswith:
      - '.bashrc'
      - '.zshrc'
      - '.profile'
      - '.bash_profile'
      - 'authorized_keys'
  condition: selection_bashrc or selection_ssh_keys or selection_write_tool
```

#### 3.5 `agent_env_manipulation.yml` (HIGH)
```yaml
id: agent-env-manip-001
title: Environment Variable Manipulation
description: Detects PATH hijacking, LD_PRELOAD injection, and sensitive env changes.
level: high
detection:
  selection_path_hijack:
    event_type: tool_call
    command|contains:
      - 'export PATH='
      - 'PATH=/'
  selection_ld_preload:
    event_type: tool_call
    command|contains:
      - 'LD_PRELOAD='
      - 'LD_LIBRARY_PATH='
  selection_env_write:
    event_type: tool_call
    command|contains:
      - 'export ANTHROPIC_API_KEY='
      - 'export AWS_SECRET_ACCESS_KEY='
      - 'export GITHUB_TOKEN='
  condition: selection_path_hijack or selection_ld_preload or selection_env_write
```

#### 3.6 `agent_encoded_payload.yml` (HIGH)
```yaml
id: agent-encoded-payload-001
title: Encoded/Obfuscated Command Execution
description: Detects base64-encoded payloads and obfuscated command execution.
level: high
detection:
  selection_base64_exec:
    event_type: tool_call
    command|contains:
      - 'base64 -d'
      - 'base64 --decode'
    command|contains:
      - '| bash'
      - '| sh'
      - '| python'
  selection_python_exec:
    event_type: tool_call
    command|re: 'python[3]?\s+-c\s+.*(__import__|exec|eval|compile)\s*\('
  selection_eval:
    event_type: tool_call
    command|contains:
      - 'eval $(echo'
      - 'eval "$(echo'
  condition: selection_base64_exec or selection_python_exec or selection_eval
```

#### 3.7 `agent_container_escape.yml` (CRITICAL)
```yaml
id: agent-container-escape-001
title: Container Escape Attempt
description: Detects patterns associated with Docker/container escape techniques.
level: critical
detection:
  selection_docker_socket:
    event_type: tool_call
    command|contains: '/var/run/docker.sock'
  selection_nsenter:
    event_type: tool_call
    command|contains: 'nsenter'
  selection_mount_host:
    event_type: tool_call
    command|contains:
      - 'mount /dev/'
      - 'mount -o bind /'
  selection_proc_escape:
    event_type: tool_call
    command|contains:
      - '/proc/1/root'
      - '/proc/sysrq-trigger'
  condition: selection_docker_socket or selection_nsenter or selection_mount_host or selection_proc_escape
```

#### 3.8 `agent_dns_tunneling.yml` (MEDIUM)
```yaml
id: agent-dns-tunnel-001
title: Potential DNS Tunneling or Encoded Data Transfer
description: Detects patterns that may indicate DNS-based data exfiltration or encoded transfers.
level: medium
detection:
  selection_dig_txt:
    event_type: tool_call
    command|contains:
      - 'dig TXT'
      - 'dig +short TXT'
      - 'nslookup -type=TXT'
  selection_long_subdomain:
    event_type: tool_call
    command|re: 'dig\s+[a-zA-Z0-9]{30,}\.'
  selection_xxd_nc:
    event_type: tool_call
    command|contains:
      - 'xxd'
      - 'od -A x'
    command|contains:
      - 'nc '
      - 'ncat '
  condition: selection_dig_txt or selection_long_subdomain or selection_xxd_nc
```

### Files to create
- 8 new YAML files in `rules/`.
- Tests for each rule in `tests/test_new_rules.py`.

---

## Plan 4: Database Connection Pooling and Hygiene

### Problem
Every database operation opens and closes a connection (`async with aiosqlite.connect(...)` in every method of `AlertStore`, `EventStore`, `FeedbackStore`). No data retention policy -- database grows unbounded.

### Goal
Single persistent connection per store instance. Automatic data retention with configurable TTL.

### Implementation

#### 4.1 Connection management: `store/database.py`

```python
class Database:
    """Shared async SQLite connection manager."""

    def __init__(self, db_path: Path) -> None:
        self.db_path = db_path
        self._conn: aiosqlite.Connection | None = None

    async def connect(self) -> None:
        """Open persistent connection with WAL mode."""
        self._conn = await aiosqlite.connect(self.db_path)
        self._conn.row_factory = aiosqlite.Row
        await self._conn.execute("PRAGMA journal_mode=WAL")
        await self._conn.execute("PRAGMA auto_vacuum=INCREMENTAL")
        await self._conn.execute("PRAGMA busy_timeout=5000")

        schema_path = Path(__file__).parent / "schema.sql"
        await self._conn.executescript(schema_path.read_text())
        await self._conn.commit()

    async def close(self) -> None:
        """Close the connection."""
        if self._conn:
            await self._conn.close()
            self._conn = None

    @property
    def conn(self) -> aiosqlite.Connection:
        assert self._conn is not None, "Database not connected"
        return self._conn

    async def __aenter__(self) -> "Database":
        await self.connect()
        return self

    async def __aexit__(self, *args) -> None:
        await self.close()
```

#### 4.2 Refactor stores to accept a `Database` instance

```python
class AlertStore:
    def __init__(self, db: Database) -> None:
        self._db = db

    async def insert(self, alert: Alert) -> None:
        await self._db.conn.execute("INSERT INTO ...", (...))
        await self._db.conn.commit()
```

All three stores (`AlertStore`, `EventStore`, `FeedbackStore`) share the same `Database` instance. One connection for the entire daemon lifetime.

#### 4.3 Data retention

Add to `Database`:

```python
async def enforce_retention(self, max_age_days: int = 90) -> int:
    """Delete events and alerts older than max_age_days. Returns rows deleted."""
    cutoff = (datetime.now(UTC) - timedelta(days=max_age_days)).isoformat()

    cursor = await self.conn.execute(
        "DELETE FROM events WHERE timestamp < ?", (cutoff,)
    )
    events_deleted = cursor.rowcount

    cursor = await self.conn.execute(
        "DELETE FROM alerts WHERE timestamp < ?", (cutoff,)
    )
    alerts_deleted = cursor.rowcount

    # Don't delete feedback -- it's training data for rule refinement
    await self.conn.execute("PRAGMA incremental_vacuum")
    await self.conn.commit()

    return events_deleted + alerts_deleted
```

Call this once per day in the daemon's main loop:

```python
# In MonitorDaemon.run()
if iteration % iterations_per_day == 0:
    deleted = await self._db.enforce_retention(config.retention_days)
    logger.info("Retention cleanup: deleted %d old records", deleted)
```

#### 4.4 Config additions
```python
# config.py
retention_days: int = 90  # How long to keep events/alerts
```

#### 4.5 Files to modify
- Create `store/database.py`.
- Modify `store/alerts.py`, `store/events.py`, `store/feedback.py` -- accept `Database` instead of `db_path`.
- Modify `daemon.py` -- create one `Database`, pass to all stores.
- Modify `cli.py` -- create `Database` in commands that access stores.
- Update `schema.sql` -- add `PRAGMA auto_vacuum=INCREMENTAL`.

---

## Plan 5: Alert Deduplication and Rate Limiting

### Problem
If the same Sigma rule fires 100 times on the same command pattern in one polling cycle, that generates 100 separate alerts and potentially 100 LLM API calls.

### Goal
Deduplicate alerts by (rule_id, command_pattern) within a configurable time window. Rate-limit LLM calls.

### Implementation

#### 5.1 New module: `src/agentshield/dedup.py`

```python
from collections import defaultdict
from datetime import datetime, timedelta

class AlertDeduplicator:
    """Deduplicate alerts by rule + command within a time window."""

    def __init__(self, window_seconds: int = 300) -> None:
        self.window = timedelta(seconds=window_seconds)
        # Key: (rule_id, normalized_command) -> last_seen_time
        self._seen: dict[tuple[str, str], datetime] = {}
        self._suppressed_count: dict[tuple[str, str], int] = defaultdict(int)

    def should_alert(self, alert: Alert) -> bool:
        """Returns True if this alert should be processed (not a duplicate)."""
        key = (alert.rule_id, self._normalize(alert.event.command))
        now = alert.timestamp

        if key in self._seen:
            last_seen = self._seen[key]
            if now - last_seen < self.window:
                self._suppressed_count[key] += 1
                return False

        self._seen[key] = now
        self._suppressed_count[key] = 0
        self._cleanup(now)
        return True

    def get_suppressed_count(self, rule_id: str, command: str | None) -> int:
        key = (rule_id, self._normalize(command))
        return self._suppressed_count.get(key, 0)

    def _normalize(self, command: str | None) -> str:
        if not command:
            return ""
        return " ".join(command.strip().lower().split())

    def _cleanup(self, now: datetime) -> None:
        """Remove entries older than 2x the window."""
        cutoff = now - (self.window * 2)
        expired = [k for k, v in self._seen.items() if v < cutoff]
        for k in expired:
            del self._seen[k]
            self._suppressed_count.pop(k, None)
```

#### 5.2 LLM rate limiter

Add a simple token bucket to `TriageAgent`:

```python
class TriageAgent:
    def __init__(self, ..., max_calls_per_minute: int = 10):
        self._call_times: list[float] = []
        self._max_per_minute = max_calls_per_minute

    async def _rate_limit(self) -> bool:
        """Returns True if we're within rate limits."""
        now = time.monotonic()
        # Remove calls older than 60 seconds
        self._call_times = [t for t in self._call_times if now - t < 60]
        if len(self._call_times) >= self._max_per_minute:
            return False
        self._call_times.append(now)
        return True
```

#### 5.3 Wire into daemon

```python
# daemon.py, in _process_logs()
alerts = self._detect_threats(events)

# Deduplicate before triage
unique_alerts = [a for a in alerts if self._deduplicator.should_alert(a)]
suppressed = len(alerts) - len(unique_alerts)
if suppressed > 0:
    logger.info("Suppressed %d duplicate alerts", suppressed)

for alert in unique_alerts:
    triaged = await self._triage_alert(alert)
    ...
```

#### 5.4 Config additions
```python
dedup_window_seconds: int = 300  # 5 minute dedup window
max_llm_calls_per_minute: int = 10
```

---

## Plan 6: Position File Race Condition Fix

### Problem
In `clawdbot.py:400-428`, multiple collectors share the same `.positions.json` file. Each collector reads the full file, modifies its own entry, and writes the whole file back. With concurrent async collectors, this is a classic read-modify-write race: collector A reads, collector B reads, A writes, B writes (clobbering A's update).

### Goal
Eliminate the race condition without adding external dependencies.

### Implementation

#### Option A: File locking (recommended)

Use `fcntl.flock` on the position file:

```python
import fcntl

async def _save_position(self, position: int) -> None:
    if not self.position_file:
        return

    self.position_file.parent.mkdir(parents=True, exist_ok=True)

    # Open with exclusive lock
    fd = os.open(str(self.position_file), os.O_RDWR | os.O_CREAT)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        with os.fdopen(fd, 'r+') as f:
            # Read current positions
            content = f.read()
            positions = json.loads(content) if content.strip() else {}

            # Update this collector's position
            positions[str(self.log_path)] = position

            # Write back
            f.seek(0)
            f.truncate()
            f.write(json.dumps(positions))
    except Exception:
        os.close(fd)
        raise
```

Note: this uses synchronous file I/O, but position file operations are tiny and infrequent enough that blocking for microseconds is acceptable.

#### Option B: Per-collector position files (simpler)

Instead of one shared `.positions.json`, each collector gets its own file:

```python
# In daemon.py, _refresh_collectors()
def _position_file_for(self, log_path: Path) -> Path:
    """Generate a unique position file for each log file."""
    # Hash the log path to create a unique, stable filename
    name_hash = hashlib.sha256(str(log_path).encode()).hexdigest()[:12]
    return self.data_dir / f".pos_{name_hash}"
```

This eliminates the race condition entirely -- no shared state. Each collector reads/writes only its own file.

**Option B is recommended** for simplicity. The shared JSON file was a premature optimization that introduced a concurrency bug.

#### Files to modify
- `daemon.py:166` -- Change position file from shared to per-collector.
- `clawdbot.py` -- Remove the read-modify-write pattern (each file is single-writer).
- `claudecode.py` -- Same change if it uses position files.

---

## Plan 7: Daemon Process Management (stop/status)

### Problem
`stop` and `status` CLI commands are stubs (`cli.py:92-100`). No PID file, no IPC. Background mode isn't implemented.

### Goal
Working `stop`, `status`, and background daemon mode using a PID file and Unix signals.

### Implementation

#### 7.1 PID file management: `src/agentshield/pidfile.py`

```python
class PidFile:
    """Manage daemon PID file."""

    def __init__(self, path: Path) -> None:
        self.path = path

    def write(self) -> None:
        """Write current PID to file."""
        self.path.write_text(str(os.getpid()))

    def read(self) -> int | None:
        """Read PID from file. Returns None if not found or stale."""
        if not self.path.exists():
            return None
        try:
            pid = int(self.path.read_text().strip())
            # Check if process is actually running
            os.kill(pid, 0)  # Signal 0 = check existence
            return pid
        except (ValueError, ProcessLookupError, PermissionError):
            self.remove()
            return None

    def remove(self) -> None:
        self.path.unlink(missing_ok=True)
```

#### 7.2 Update `start` command

```python
@app.command()
def start(foreground: bool = typer.Option(False, "-f", "--foreground")):
    config = load_config()
    pidfile = PidFile(config.data_dir / "agentshield.pid")

    # Check if already running
    existing_pid = pidfile.read()
    if existing_pid:
        console.print(f"AgentShield is already running (PID {existing_pid})")
        raise typer.Exit(1)

    if foreground:
        pidfile.write()
        try:
            _run_async(run_daemon(...))
        finally:
            pidfile.remove()
    else:
        # Background mode: fork and detach
        pid = os.fork()
        if pid > 0:
            console.print(f"AgentShield started (PID {pid})")
            return
        # Child process
        os.setsid()
        pidfile.write()
        # Redirect stdout/stderr to log file
        log_file = config.data_dir / "daemon.log"
        ...
        asyncio.run(run_daemon(...))
```

#### 7.3 Update `stop` command

```python
@app.command()
def stop():
    config = load_config()
    pidfile = PidFile(config.data_dir / "agentshield.pid")

    pid = pidfile.read()
    if pid is None:
        console.print("AgentShield is not running.")
        raise typer.Exit(1)

    os.kill(pid, signal.SIGTERM)
    # Wait up to 5 seconds for graceful shutdown
    for _ in range(50):
        try:
            os.kill(pid, 0)
            time.sleep(0.1)
        except ProcessLookupError:
            pidfile.remove()
            console.print("AgentShield stopped.")
            return

    # Force kill if still running
    os.kill(pid, signal.SIGKILL)
    pidfile.remove()
    console.print("AgentShield force-stopped.")
```

#### 7.4 Update `status` command

```python
@app.command()
def status():
    config = load_config()
    pidfile = PidFile(config.data_dir / "agentshield.pid")

    pid = pidfile.read()
    if pid is None:
        console.print("AgentShield is [bold red]not running[/bold red]")
        raise typer.Exit(1)

    console.print(f"AgentShield is [bold green]running[/bold green] (PID {pid})")

    # Show quick stats from DB
    async def show_stats():
        db = Database(config.db_path)
        await db.connect()
        alert_store = AlertStore(db)
        recent = await alert_store.get_recent(5)
        console.print(f"Recent alerts: {len(recent)}")
        await db.close()

    _run_async(show_stats())
```

#### 7.5 Files to modify
- Create `pidfile.py`.
- Modify `cli.py` -- Update `start`, `stop`, `status` commands.
- Modify `daemon.py` -- Clean up PID file on signal-based shutdown.

---

## Plan 8: MCP Server Input Validation

### Problem
The MCP server accepts `alert_id`, `rule_id`, `level`, `timestamp`, `command` from external agents without validation (`cli.py:579-630`). While parameterized SQL queries prevent injection, invalid data will cause runtime errors or corrupted records.

### Goal
Validate all MCP inputs at the boundary. Fail fast with clear error messages.

### Implementation

#### 8.1 Pydantic models for MCP inputs

Add to `mcp/server.py`:

```python
from pydantic import BaseModel, field_validator
from agentshield.models.alerts import AlertLevel

class ReceiveAlertInput(BaseModel):
    alert_id: str
    rule_id: str
    rule_name: str
    level: str
    event_type: str
    command: str
    timestamp: str
    working_dir: str = ""

    @field_validator("level")
    @classmethod
    def validate_level(cls, v: str) -> str:
        valid = {"low", "medium", "high", "critical"}
        if v.lower() not in valid:
            raise ValueError(f"level must be one of {valid}")
        return v.lower()

    @field_validator("timestamp")
    @classmethod
    def validate_timestamp(cls, v: str) -> str:
        try:
            datetime.fromisoformat(v.replace("Z", "+00:00"))
        except ValueError:
            raise ValueError("timestamp must be a valid ISO 8601 string")
        return v

    @field_validator("alert_id", "rule_id")
    @classmethod
    def validate_ids(cls, v: str) -> str:
        if not v or len(v) > 256:
            raise ValueError("ID must be between 1 and 256 characters")
        # Only allow safe characters
        if not re.match(r'^[\w\-\.]+$', v):
            raise ValueError("ID contains invalid characters")
        return v

    @field_validator("command")
    @classmethod
    def validate_command(cls, v: str) -> str:
        if len(v) > 10000:
            raise ValueError("command exceeds maximum length of 10000")
        # Strip control characters except newline and tab
        return re.sub(r'[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]', '', v)

class SubmitFeedbackInput(BaseModel):
    alert_id: str
    feedback: str
    comment: str = ""

    @field_validator("feedback")
    @classmethod
    def validate_feedback(cls, v: str) -> str:
        valid = {"safe", "threat"}
        if v.lower() not in valid:
            raise ValueError(f"feedback must be one of {valid}")
        return v.lower()
```

#### 8.2 Apply validation in MCP tool handlers

```python
@mcp.tool()
async def receive_alert(**kwargs) -> dict:
    try:
        validated = ReceiveAlertInput(**kwargs)
    except ValidationError as e:
        return {"error": str(e), "status": "invalid_input"}
    return await server.receive_alert(**validated.model_dump())
```

#### 8.3 Files to modify
- `mcp/server.py` -- Add validation models, apply in handlers.
- `cli.py` -- Update MCP tool registrations to validate before calling server methods.

---

## Plan 9: Position File Per-Collector (see Plan 6)

*Merged into Plan 6 above. The race condition fix and the per-collector position file approach are the same solution.*

---

## Plan 10: Credential Access Rule Logic Fix

### Problem
In `agent_credential_access.yml`, `selection_cat_ssh` (lines 47-53) requires BOTH `command|contains` of `cat/less/head/tail` AND `command|contains` of `.ssh/`. This works for `cat ~/.ssh/id_rsa` but misses `grep -r password .ssh/` or `cp .ssh/id_rsa /tmp/`. The rule is too narrow -- it only catches 4 specific read commands.

More broadly, the rule doesn't catch:
- `scp` of credential files to remote hosts
- `tar` or `zip` of credential directories
- Reading credentials via Python/Node scripts
- `env` or `printenv` for dumping environment variables with secrets

### Goal
Fix the logic error and broaden coverage without creating excessive false positives.

### Implementation

#### 10.1 Restructure the rule

Split into two clear categories: **file-based access** (any tool reading sensitive paths) and **command-based access** (commands that target credentials regardless of the specific binary used).

```yaml
id: agent-credential-access-001
title: Credential File Access Attempt
description: |
  Detects access to sensitive credential files including environment files,
  SSH keys, cloud provider credentials, and keychain access.
level: high
detection:
  # Direct file reads (from file_read events, e.g., Read tool)
  selection_file_read_env:
    event_type: file_read
    file_path|endswith: '.env'
  selection_file_read_ssh:
    event_type: file_read
    file_path|contains: '.ssh/'
  selection_file_read_cloud:
    event_type: file_read
    file_path|contains:
      - '.aws/credentials'
      - '.aws/config'
      - '.config/gcloud/'
      - '.kube/config'
      - '.docker/config.json'

  # Command-based credential access (broader binary matching)
  selection_cmd_env:
    event_type: tool_call
    command|contains: '.env'
    command|re: '(cat|less|head|tail|grep|cp|scp|tar|zip|mv|xargs)\s'
  selection_cmd_ssh:
    event_type: tool_call
    command|contains: '.ssh/'
    command|re: '(cat|less|head|tail|grep|cp|scp|tar|zip|mv|xargs)\s'
  selection_cmd_cloud:
    event_type: tool_call
    command|contains:
      - '.aws/credentials'
      - '.kube/config'
    command|re: '(cat|less|head|tail|grep|cp|scp|tar|zip)\s'

  # Environment variable dumping
  selection_env_dump:
    event_type: tool_call
    command|re: '(^env$|^printenv|^set$|export\s+-p)'

  # macOS keychain
  selection_keychain:
    event_type: tool_call
    command|contains:
      - 'security find-generic-password'
      - 'security dump-keychain'

  condition: >
    selection_file_read_env or selection_file_read_ssh or selection_file_read_cloud
    or selection_cmd_env or selection_cmd_ssh or selection_cmd_cloud
    or selection_env_dump or selection_keychain
```

#### 10.2 Files to modify
- `rules/agent_credential_access.yml` -- Replace with restructured rule.
- Update tests in `tests/test_initial_sigma_rules.py` to cover new patterns.

---

## Priority Order for Implementation

| Priority | Plan | Effort | Impact |
|----------|------|--------|--------|
| 1 | **Plan 1: Pre-Execution Hooks** | High | Transforms from monitor to shield |
| 2 | **Plan 3: Expanded Rules** | Low | Immediate detection coverage |
| 3 | **Plan 2: Local-First Triage** | Medium | Removes LLM dependency for 90% of cases |
| 4 | **Plan 5: Alert Dedup** | Low | Prevents alert storms and API waste |
| 5 | **Plan 10: Credential Rule Fix** | Low | Fixes existing detection gap |
| 6 | **Plan 4: DB Connection Pooling** | Medium | Performance and data hygiene |
| 7 | **Plan 6: Position File Race** | Low | Correctness fix |
| 8 | **Plan 7: Daemon Management** | Medium | Operational completeness |
| 9 | **Plan 8: MCP Validation** | Low | Input safety at boundary |

Plans 3, 5, 6, 8, and 10 are small, independent changes that can be done in parallel. Plans 1, 2, and 4 are larger architectural changes that should be done sequentially.
