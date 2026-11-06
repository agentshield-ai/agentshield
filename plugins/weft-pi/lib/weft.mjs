// Shared core for the Weft agent harness plugins (Claude Code, Codex, OpenClaw, Pi).
//
// The canonical copy lives in plugins/weft-harness-common/weft.mjs and is
// vendored into each plugin as lib/weft.mjs (scripts: `node
// plugins/weft-harness-common/sync.mjs`); a test checks the copies match.
// Plain Node (>= 18) ESM with no dependencies, so plugins run without a build.
//
// It builds Weft spans in the shapes the server's agents index understands
// (turns are `invoke_agent` spans, LLM requests `chat` spans, tools
// `execute_tool` spans; conversation = the span's session_id) and sends them
// to Weft's span batch route, POST /api/v1/spans/batch, with a short timeout.
// Span ids are derived from stable harness ids, so a resend after a failure
// upserts the same spans.

import { createHash, randomUUID } from 'node:crypto';
import { appendFileSync, chmodSync, closeSync, copyFileSync, existsSync, mkdirSync, openSync, readdirSync, readFileSync, renameSync, statSync, unlinkSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';

export const CORE_VERSION = '0.1.0';
export const DEFAULT_SERVER_URL = 'http://localhost:6345';

const TRUE = new Set(['1', 'true', 'yes', 'on']);
const FALSE = new Set(['0', 'false', 'no', 'off']);

export function envBool(v, dflt) {
  if (v === undefined || v === null || v === '') return dflt;
  const s = String(v).trim().toLowerCase();
  if (TRUE.has(s)) return true;
  if (FALSE.has(s)) return false;
  return dflt;
}

function readJson(path) {
  try {
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch {
    return {};
  }
}

/**
 * Plugin configuration. Precedence: explicit overrides, environment, the
 * plugin's settings file (`<stateDir>/settings.json`), defaults.
 *
 *   WEFT_SERVER_URL, WEFT_API_KEY, WEFT_PROJECT (required; `entity/project`),
 *   WEFT_AGENT_NAME, WEFT_CAPTURE_CONTENT (default true),
 *   WEFT_HOOK_TIMEOUT_MS (network timeout, default 2000),
 *   WEFT_MAX_FIELD_CHARS (default 20000), WEFT_PLUGIN_DISABLED, WEFT_DEBUG,
 *   WEFT_STATE_RETENTION_DAYS (local session logs left behind by sessions that
 *   never ended are deleted after this many days, default 7; 0 keeps them).
 */
export function loadConfig({ plugin, defaultAgent, stateDirEnv, env = process.env, overrides = {} } = {}) {
  const stateDir = overrides.stateDir ?? env[stateDirEnv ?? ''] ?? env.WEFT_PLUGIN_STATE_DIR ?? join(homedir(), `.${plugin ?? 'weft-plugin'}`);
  const file = readJson(join(stateDir, 'settings.json'));
  const pick = (key, envName, dflt) => overrides[key] ?? (env[envName] !== undefined && env[envName] !== '' ? env[envName] : undefined) ?? file[key] ?? dflt;
  const cfg = {
    plugin: plugin ?? 'weft-plugin',
    stateDir,
    serverUrl: String(pick('serverUrl', 'WEFT_SERVER_URL', file.server_url ?? DEFAULT_SERVER_URL)).replace(/\/+$/, ''),
    apiKey: pick('apiKey', 'WEFT_API_KEY', file.api_key ?? ''),
    project: pick('project', 'WEFT_PROJECT', file.project ?? ''),
    agentName: String(pick('agentName', 'WEFT_AGENT_NAME', file.agent_name ?? defaultAgent ?? 'agent')).trim() || defaultAgent || 'agent',
    captureContent: envBool(pick('captureContent', 'WEFT_CAPTURE_CONTENT', file.capture_content), true),
    timeoutMs: Number(pick('timeoutMs', 'WEFT_HOOK_TIMEOUT_MS', file.timeout_ms ?? 2000)) || 2000,
    maxFieldChars: Number(pick('maxFieldChars', 'WEFT_MAX_FIELD_CHARS', file.max_field_chars ?? 20000)) || 20000,
    disabled: envBool(pick('disabled', 'WEFT_PLUGIN_DISABLED', file.disabled), false),
    debug: envBool(pick('debug', 'WEFT_DEBUG', file.debug), false),
    retentionDays: Number(pick('retentionDays', 'WEFT_STATE_RETENTION_DAYS', file.state_retention_days ?? 7)),
  };
  if (!Number.isFinite(cfg.retentionDays) || cfg.retentionDays < 0) cfg.retentionDays = 7;
  if (!cfg.project) cfg.disabled = true;
  return cfg;
}

// ---------------------------------------------------------------- private local state
//
// Plugin state (session logs, cursors, logs) can hold prompts and tool output,
// so it lives in directories only the user can read (0700) and files created
// 0600; directories made earlier with wider modes are tightened.

/** mkdir -p with mode 0700, tightening `dir` itself when it already exists. */
export function privateMkdir(dir) {
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  try {
    if ((statSync(dir).mode & 0o077) !== 0) chmodSync(dir, 0o700);
  } catch {
    /* not ours to change */
  }
}

function tighten(path) {
  try {
    if ((statSync(path).mode & 0o077) !== 0) chmodSync(path, 0o600);
  } catch {
    /* ignore */
  }
}

/** Append to a file created 0600 (its directory 0700). */
export function appendPrivate(path, text) {
  privateMkdir(dirname(path));
  appendFileSync(path, text, { mode: 0o600 });
  tighten(path);
}

/** Replace a file atomically (temp file + rename), created 0600. */
export function writePrivate(path, text) {
  privateMkdir(dirname(path));
  const tmp = `${path}.${process.pid}.${randomUUID().slice(0, 8)}.tmp`;
  try {
    writeFileSync(tmp, text, { mode: 0o600 });
    renameSync(tmp, path);
  } catch (e) {
    try {
      unlinkSync(tmp);
    } catch {
      /* ignore */
    }
    throw e;
  }
}

/** Create a file exclusively (a lock), 0600. Throws EEXIST when it exists. */
export function createPrivateExclusive(path) {
  privateMkdir(dirname(path));
  closeSync(openSync(path, 'wx', 0o600));
}

/**
 * Delete files in `dir` whose names match `re` and that were last changed more
 * than `maxAgeDays` days ago (session logs of crashed sessions). Returns the
 * names deleted. Never throws.
 */
export function pruneOldFiles(dir, re, maxAgeDays = 14, now = Date.now()) {
  const out = [];
  if (!(maxAgeDays > 0)) return out;
  const cut = now - maxAgeDays * 86_400_000;
  let names = [];
  try {
    names = readdirSync(dir);
  } catch {
    return out;
  }
  for (const n of names) {
    if (!re.test(n)) continue;
    try {
      const p = join(dir, n);
      if (statSync(p).mtimeMs < cut) {
        unlinkSync(p);
        out.push(n);
      }
    } catch {
      /* raced or not ours */
    }
  }
  return out;
}

// ---------------------------------------------------------------- logging

/** Append a line to `<stateDir>/logs/<name>.log` (rotated at 1 MB, 0600 in a 0700 dir). Never throws. */
export function makeLogger(cfg, name = 'plugin') {
  const path = join(cfg.stateDir, 'logs', `${name}.log`);
  const write = (level, msg) => {
    if (level === 'debug' && !cfg.debug) return;
    try {
      privateMkdir(cfg.stateDir);
      try {
        if (statSync(path).size > 1 << 20) renameSync(path, `${path}.1`);
      } catch {
        /* no file yet */
      }
      appendPrivate(path, `${new Date().toISOString()} ${level} ${msg}\n`);
    } catch {
      /* logging must never fail the hook */
    }
  };
  return { path, debug: (m) => write('debug', m), info: (m) => write('info', m), warn: (m) => write('warn', m), error: (m) => write('error', m) };
}

// ---------------------------------------------------------------- ids and time

/** A UUID derived from the parts (stable across processes and retries). */
export function stableId(...parts) {
  const h = createHash('sha256').update(parts.map((p) => String(p ?? '')).join('\u0000')).digest('hex');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-${((parseInt(h[16], 16) & 3) | 8).toString(16)}${h.slice(17, 20)}-${h.slice(20, 32)}`;
}

export function newId() {
  return randomUUID();
}

/** ISO timestamp from a Date, epoch ms, or a parseable string (now when invalid). */
export function iso(t) {
  if (t instanceof Date) return t.toISOString();
  if (typeof t === 'number' && Number.isFinite(t)) return new Date(t).toISOString();
  if (typeof t === 'string' && t) {
    const d = new Date(t);
    if (!Number.isNaN(d.getTime())) return d.toISOString();
  }
  return new Date().toISOString();
}

// ---------------------------------------------------------------- values

/** Cap strings (and very large values) so a tool result never bloats a span. */
export function truncate(value, max = 20000, depth = 0) {
  if (typeof value === 'string') return value.length > max ? `${value.slice(0, max)}… [truncated ${value.length - max} chars]` : value;
  if (value === null || typeof value !== 'object') return value;
  if (depth > 12) return '[depth limit]';
  if (Array.isArray(value)) {
    const out = value.slice(0, 500).map((v) => truncate(v, max, depth + 1));
    if (value.length > 500) out.push(`… [${value.length - 500} more items]`);
    return out;
  }
  const out = {};
  let n = 0;
  for (const [k, v] of Object.entries(value)) {
    if (n++ >= 500) {
      out['…'] = 'more keys truncated';
      break;
    }
    out[k] = truncate(v, max, depth + 1);
  }
  return out;
}

export function parseMaybeJson(v) {
  if (typeof v !== 'string') return v;
  const s = v.trim();
  if (!s || (s[0] !== '{' && s[0] !== '[')) return v;
  try {
    return JSON.parse(s);
  } catch {
    return v;
  }
}

export function shortText(v, n = 60) {
  let s = typeof v === 'string' ? v : v == null ? '' : JSON.stringify(v) ?? '';
  s = s.replace(/\s+/g, ' ').trim();
  return s.length <= n ? s : `${s.slice(0, n - 1)}…`;
}

/** Display name for a tool span: the tool plus its most telling argument. */
export function toolDisplay(name, input) {
  if (input && typeof input === 'object') {
    for (const k of ['command', 'cmd', 'file_path', 'path', 'pattern', 'url', 'query', 'description', 'prompt']) {
      const v = input[k];
      if (v) return `${name}: ${shortText(Array.isArray(v) ? v.join(' ') : v, 50)}`;
    }
  }
  return name;
}

// ---------------------------------------------------------------- spans

/** gen_ai.* attributes that carry conversation content (dropped when content capture is off). */
export const CONTENT_GENAI_KEYS = new Set(['gen_ai.conversation.name', 'gen_ai.prompt', 'gen_ai.completion', 'gen_ai.input.messages', 'gen_ai.output.messages', 'gen_ai.system_instructions']);
/** Harness attribute keys (under any namespace) naming local paths or content (dropped when content capture is off). */
export const CONTENT_ATTRIBUTE_KEYS = new Set(['cwd', 'transcript_path', 'agent_transcript_path', 'session_file', 'parent_session_file', 'prompt', 'last_assistant_message']);

/** A display name that carries no content: the tool name, `chat <model>` or `invoke_agent <agent>`. */
export function safeDisplayName(kind, genai = {}, name = '') {
  if (kind === 'tool') return String(genai['gen_ai.tool.name'] ?? name);
  if (kind === 'llm') return `chat ${genai['gen_ai.request.model'] ?? ''}`.trim();
  if (kind === 'agent') return `invoke_agent ${genai['gen_ai.agent.name'] ?? ''}`.trim();
  return String(name);
}

const GENERIC_ERROR = { tool: 'tool error', llm: 'llm error', agent: 'agent error' };

/**
 * One finished span as a batch item for POST /api/v1/spans/batch
 * (`{span: {...}}`: start and end in one piece).
 * `kind` is the span kind (agent, llm, tool); `genai` are flat gen_ai.* attributes;
 * `sessionId` is the conversation (gen_ai.conversation.id), `turnId` the turn.
 *
 * With `cfg.captureContent` false nothing the user or the model wrote leaves
 * the machine: inputs and output are empty, the display name is
 * `safeDisplayName` (or `c.safeDisplayName`), the error is `c.errorKind` (a
 * fixed, content-free class such as "interrupted") or a generic "tool error",
 * and `gen_ai.conversation.name` and local paths (`cwd`, `transcript_path`, ...)
 * are dropped.
 */
export function spanItems(cfg, c) {
  const kind = c.kind;
  const capture = cfg.captureContent !== false;
  const harness = { ...(c.attributes ?? {}) };
  if (!capture) {
    for (const [ns, v] of Object.entries(harness)) {
      if (ns === 'weft' || !v || typeof v !== 'object' || Array.isArray(v)) continue;
      harness[ns] = Object.fromEntries(Object.entries(v).filter(([k]) => !CONTENT_ATTRIBUTE_KEYS.has(k)));
    }
  }
  const attributes = {
    ...harness,
    weft: { span_kind: kind, source: cfg.plugin, plugin_version: c.pluginVersion ?? CORE_VERSION, ...(c.attributes?.weft ?? {}) },
  };
  for (const [k, v] of Object.entries(c.genai ?? {})) {
    if (v === undefined || v === null || v === '') continue;
    if (!capture && CONTENT_GENAI_KEYS.has(k)) continue;
    attributes[k] = v;
  }
  const display = capture ? (c.displayName ?? null) : (c.safeDisplayName ?? safeDisplayName(kind, c.genai ?? {}, c.name));
  const span = {
    project_id: cfg.project,
    id: c.id,
    name: c.name,
    display_name: display,
    trace_id: c.traceId,
    parent_id: c.parentId ?? null,
    started_at: iso(c.startedAt),
    attributes,
    inputs: capture ? truncate(c.inputs ?? {}, cfg.maxFieldChars) : {},
    ended_at: iso(c.endedAt ?? c.startedAt),
    output: capture ? truncate(c.output ?? null, cfg.maxFieldChars) : null,
    summary: c.summary ?? {},
  };
  if (c.sessionId) span.session_id = c.sessionId;
  if (c.turnId) span.turn_id = c.turnId;
  if (c.error) span.error = capture ? String(c.error).slice(0, 4000) : String(c.errorKind ?? GENERIC_ERROR[kind] ?? 'error');
  return [{ span }];
}

/** `summary.usage` for one LLM request. */
export function usageSummary(model, u) {
  if (!u) return {};
  const prompt = Number(u.prompt ?? 0) || 0;
  const completion = Number(u.completion ?? 0) || 0;
  if (!prompt && !completion) return {};
  const e = { requests: 1, prompt_tokens: prompt, completion_tokens: completion, total_tokens: Number(u.total ?? 0) || prompt + completion };
  if (u.cacheRead) e.cache_read_input_tokens = u.cacheRead;
  if (u.cacheWrite) e.cache_creation_input_tokens = u.cacheWrite;
  if (u.reasoning) e.reasoning_tokens = u.reasoning;
  return { usage: { [model || 'unknown']: e } };
}

/** Chat-completion shaped output for an assistant message (content: string or blocks). */
export function chatOutput(model, content, finishReason) {
  return { object: 'chat.completion', model: model || undefined, choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: finishReason ?? null }] };
}

// ---------------------------------------------------------------- sending

/** True for localhost, *.localhost and loopback IP literals. */
export function isLoopbackHost(host) {
  const h = String(host ?? '').replace(/^\[|\]$/g, '').toLowerCase();
  return h === 'localhost' || h.endsWith('.localhost') || /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(h) || h === '::1';
}

/** The warning to log when the API key would go over plain http to a non-loopback host, else null. */
export function plainHttpKeyWarning(cfg) {
  if (!cfg?.apiKey) return null;
  let u;
  try {
    u = new URL(cfg.serverUrl);
  } catch {
    return null;
  }
  if (u.protocol !== 'http:' || isLoopbackHost(u.hostname)) return null;
  return `sending the Weft API key to ${u.origin} over plain http; anyone on the network path can read it. Use https.`;
}

const warnedPlainHttp = new Set();

/**
 * POST span items to /api/v1/spans/batch (in chunks) with a timeout.
 * Throws on a network error, a redirect (never followed: it would resend the
 * API key and the spans to another host) or a non-2xx status. `warn` gets a
 * one-time warning when the API key goes over plain http to a non-loopback host.
 */
export async function sendItems(cfg, items, { fetchImpl = globalThis.fetch, timeoutMs, warn } = {}) {
  if (!items.length) return;
  const plain = plainHttpKeyWarning(cfg);
  if (plain && warn && !warnedPlainHttp.has(cfg.serverUrl)) {
    warnedPlainHttp.add(cfg.serverUrl);
    try {
      warn(plain);
    } catch {
      /* ignore */
    }
  }
  // X-Weft-Source declares the plugin's session source: without it Weft does
  // not rank these spans above the harness's own telemetry (docs/weft/API.md).
  const headers = { 'Content-Type': 'application/json', 'User-Agent': `${cfg.plugin}/${CORE_VERSION}`, 'X-Weft-Source': cfg.plugin };
  if (cfg.apiKey) headers.Authorization = `Bearer ${cfg.apiKey}`;
  for (let i = 0; i < items.length; i += 200) {
    const chunk = items.slice(i, i + 200);
    const res = await fetchImpl(`${cfg.serverUrl}/api/v1/spans/batch`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ project_id: cfg.project, items: chunk }),
      redirect: 'error',
      signal: AbortSignal.timeout(timeoutMs ?? cfg.timeoutMs),
    });
    if (!res.ok) {
      let text = '';
      try {
        text = (await res.text()).slice(0, 300);
      } catch {
        /* ignore */
      }
      throw new Error(`weft server returned ${res.status}: ${text}`);
    }
  }
}

// ---------------------------------------------------------------- files

/** Read a JSONL file from a byte offset; returns parsed lines (bad lines skipped) and the new offset. */
export function readJsonl(path, offset = 0) {
  let buf;
  try {
    buf = readFileSync(path);
  } catch {
    return { lines: [], offset, size: 0 };
  }
  const start = Math.min(offset, buf.length);
  const end = buf.lastIndexOf(0x0a) + 1; // only whole lines
  if (end <= start) return { lines: [], offset: start, size: buf.length };
  const lines = [];
  let pos = start;
  for (const raw of buf.subarray(start, end).toString('utf8').split('\n')) {
    const lineStart = pos;
    pos += Buffer.byteLength(raw, 'utf8') + 1;
    if (!raw.trim()) continue;
    try {
      lines.push({ ...JSON.parse(raw), __offset: lineStart });
    } catch {
      /* skip partial or corrupt lines */
    }
  }
  return { lines, offset: end, size: buf.length };
}

// ---------------------------------------------------------------- installers

/**
 * Characters allowed in a path written into a hook command. Hook commands run
 * through a shell, so anything else (quotes, `$`, backticks, `;`, `&`, `|`,
 * `<`, `>`, `%`, `!`, newlines ...) is refused instead of escaped.
 */
const SAFE_PATH = /^[A-Za-z0-9 _./:\\@+,=~()[\]{}#-]+$/;

/** Quote one argument for the shell that runs hook commands (refuses unsafe characters). */
export function shellQuote(arg, platform = process.platform) {
  const s = String(arg);
  if (!s || !SAFE_PATH.test(s)) {
    throw new Error(`refusing to write a hook command for the path ${JSON.stringify(s)}: it contains characters a shell would interpret; move the plugin to a plain directory`);
  }
  return platform === 'win32' ? `"${s}"` : `'${s}'`;
}

/** The hook command `node '<hook path>'`, safely quoted. */
export function hookCommand(hookPath, platform = process.platform) {
  return `node ${shellQuote(hookPath, platform)}`;
}

/**
 * Write `text` to `path` atomically (temp file in the same directory, then
 * rename), keeping the mode of an existing file (new files: 0600).
 */
export function writeFileAtomic(path, text) {
  mkdirSync(dirname(path), { recursive: true });
  let mode = 0o600;
  try {
    mode = statSync(path).mode & 0o777;
  } catch {
    /* new file */
  }
  const tmp = `${path}.${process.pid}.${randomUUID().slice(0, 8)}.tmp`;
  try {
    writeFileSync(tmp, text, { mode });
    chmodSync(tmp, mode);
    renameSync(tmp, path);
  } catch (e) {
    try {
      unlinkSync(tmp);
    } catch {
      /* ignore */
    }
    throw e;
  }
}

/**
 * Back up `path` to `<path>.bak` the first time only: a backup made before the
 * first install is never overwritten by later runs. Returns the backup path
 * when one was written, null otherwise.
 */
export function backupOnce(path) {
  if (!existsSync(path)) return null;
  const bak = `${path}.bak`;
  if (existsSync(bak)) return null;
  copyFileSync(path, bak);
  return bak;
}

/** Read a secret from stdin (first line, trimmed). Never echoes it. */
export async function readSecretFromStdin(stdin = process.stdin) {
  let data = '';
  for await (const chunk of stdin) {
    data += chunk;
    if (data.includes('\n')) break;
  }
  return data.split(/\r?\n/)[0].trim();
}
