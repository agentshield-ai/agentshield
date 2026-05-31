import { readFileSync } from "node:fs";

import type { AgentShieldConfig, DownstreamTransport } from "./types.js";

/**
 * Default intercept list for the MCP gateway.
 *
 * Mirrors the canonical names plus the broad Hermes platform-name list, so the
 * gateway intercepts dangerous tools regardless of which MCP server named them.
 * Canonical names also match via the pipeline's intercept check (Section 3
 * step 3), so a downstream `read_file` whose canonical form is `read` is caught
 * by either `read_file` or `read`.
 */
const DEFAULT_INTERCEPT: string[] = [
  // Canonical names
  "exec",
  "write",
  "edit",
  "read",
  "browser",
  "message",
  "sessions_spawn",
  "code_execute",
  // Common MCP / platform names
  "terminal",
  "execute_command",
  "run_command",
  "shell",
  "bash",
  "write_file",
  "create_file",
  "edit_file",
  "patch_file",
  "read_file",
  "web_browse",
  "send_message",
  "delegate",
  "spawn_agent",
  "python_execute",
];

const DEFAULT_SKIP: string[] = ["todo", "memory_search", "session_status"];

const DEFAULTS: AgentShieldConfig = {
  enabled: true,
  endpoint: "http://127.0.0.1:8433/api/v1/evaluate",
  auth_token: "",
  timeout_ms: 200,
  timeout_policy: "block",
  intercept: DEFAULT_INTERCEPT,
  skip: DEFAULT_SKIP,
  notify: "high",
  circuit_breaker: {
    failure_threshold: 3,
    recovery_interval_ms: 30_000,
  },
  downstream: {
    transport: "stdio",
    command: null,
    args: [],
    env: {},
    url: null,
  },
  serve: {
    transport: "stdio",
    port: 8434,
    host: "127.0.0.1",
  },
};

const VALID_TIMEOUT_POLICIES = new Set(["allow", "block", "log"]);
const VALID_NOTIFY_LEVELS = new Set(["all", "high", "critical", "none"]);
const VALID_TRANSPORTS = new Set<DownstreamTransport>(["stdio", "http"]);

function asString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function asStringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }
  return value.filter((v): v is string => typeof v === "string" && v.trim().length > 0);
}

/**
 * Parse and validate gateway configuration.
 *
 * Layering (lowest to highest precedence): defaults < JSON file < environment.
 * `raw` is the parsed JSON config object; `env` is the process environment.
 * All values are validated against the contract ranges (Section 4); invalid
 * values fall back to defaults silently rather than throwing, matching the
 * reference connectors.
 */
export function parseConfig(
  raw: Record<string, unknown> | undefined,
  env: NodeJS.ProcessEnv = process.env,
): AgentShieldConfig {
  const cfg = raw && typeof raw === "object" ? raw : {};

  const enabled =
    typeof cfg.enabled === "boolean"
      ? cfg.enabled
      : env.AGENTSHIELD_ENABLED === "false"
        ? false
        : DEFAULTS.enabled;

  const endpoint =
    asString(env.AGENTSHIELD_ENDPOINT) ?? asString(cfg.endpoint) ?? DEFAULTS.endpoint;

  // Auth token: explicit config > env var > empty (contract Section 4).
  const cfgToken = typeof cfg.auth_token === "string" ? cfg.auth_token : "";
  const auth_token = cfgToken || env.AGENTSHIELD_AUTH_TOKEN || DEFAULTS.auth_token;

  const rawTimeout =
    env.AGENTSHIELD_TIMEOUT_MS !== undefined
      ? Number(env.AGENTSHIELD_TIMEOUT_MS)
      : cfg.timeout_ms;
  const timeout_ms =
    typeof rawTimeout === "number" &&
    Number.isFinite(rawTimeout) &&
    rawTimeout >= 5 &&
    rawTimeout <= 5000
      ? Math.round(rawTimeout)
      : DEFAULTS.timeout_ms;

  const rawPolicy = asString(env.AGENTSHIELD_TIMEOUT_POLICY) ?? cfg.timeout_policy;
  const timeout_policy =
    typeof rawPolicy === "string" && VALID_TIMEOUT_POLICIES.has(rawPolicy)
      ? (rawPolicy as AgentShieldConfig["timeout_policy"])
      : DEFAULTS.timeout_policy;

  const rawNotify = asString(env.AGENTSHIELD_NOTIFY) ?? cfg.notify;
  const notify =
    typeof rawNotify === "string" && VALID_NOTIFY_LEVELS.has(rawNotify)
      ? (rawNotify as AgentShieldConfig["notify"])
      : DEFAULTS.notify;

  const intercept = asStringArray(cfg.intercept) ?? DEFAULTS.intercept;
  const skip = asStringArray(cfg.skip) ?? DEFAULTS.skip;

  const rawCb =
    cfg.circuit_breaker && typeof cfg.circuit_breaker === "object" && !Array.isArray(cfg.circuit_breaker)
      ? (cfg.circuit_breaker as Record<string, unknown>)
      : {};

  const circuit_breaker = {
    failure_threshold:
      typeof rawCb.failure_threshold === "number" && rawCb.failure_threshold >= 1
        ? Math.round(rawCb.failure_threshold)
        : DEFAULTS.circuit_breaker.failure_threshold,
    recovery_interval_ms:
      typeof rawCb.recovery_interval_ms === "number" && rawCb.recovery_interval_ms >= 1000
        ? Math.round(rawCb.recovery_interval_ms)
        : DEFAULTS.circuit_breaker.recovery_interval_ms,
  };

  const downstream = parseDownstream(cfg.downstream, env);
  const serve = parseServe(cfg.serve, env);

  return {
    enabled,
    endpoint,
    auth_token,
    timeout_ms,
    timeout_policy,
    intercept,
    skip,
    notify,
    circuit_breaker,
    downstream,
    serve,
  };
}

function parseDownstream(
  raw: unknown,
  env: NodeJS.ProcessEnv,
): AgentShieldConfig["downstream"] {
  const d = raw && typeof raw === "object" && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {};

  const rawTransport = asString(env.MCP_DOWNSTREAM_TRANSPORT) ?? d.transport;
  const transport =
    typeof rawTransport === "string" && VALID_TRANSPORTS.has(rawTransport as DownstreamTransport)
      ? (rawTransport as DownstreamTransport)
      : DEFAULTS.downstream.transport;

  // Command may be supplied as a single env string ("node server.js --flag");
  // we keep it as the bare command and rely on args for the rest.
  const command = asString(env.MCP_DOWNSTREAM_COMMAND) ?? asString(d.command) ?? null;

  let args: string[] = DEFAULTS.downstream.args;
  if (env.MCP_DOWNSTREAM_ARGS) {
    args = env.MCP_DOWNSTREAM_ARGS.split(" ").filter((a) => a.length > 0);
  } else {
    args = asStringArray(d.args) ?? DEFAULTS.downstream.args;
  }

  let envVars: Record<string, string> = {};
  if (d.env && typeof d.env === "object" && !Array.isArray(d.env)) {
    for (const [k, v] of Object.entries(d.env as Record<string, unknown>)) {
      if (typeof v === "string") {
        envVars[k] = v;
      }
    }
  }

  const url = asString(env.MCP_DOWNSTREAM_URL) ?? asString(d.url) ?? null;

  return { transport, command, args, env: envVars, url };
}

function parseServe(raw: unknown, env: NodeJS.ProcessEnv): AgentShieldConfig["serve"] {
  const s = raw && typeof raw === "object" && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {};

  const rawTransport = asString(env.MCP_SERVE_TRANSPORT) ?? s.transport;
  const transport =
    typeof rawTransport === "string" && VALID_TRANSPORTS.has(rawTransport as DownstreamTransport)
      ? (rawTransport as DownstreamTransport)
      : DEFAULTS.serve.transport;

  const rawPort = env.MCP_SERVE_PORT !== undefined ? Number(env.MCP_SERVE_PORT) : s.port;
  const port =
    typeof rawPort === "number" && Number.isFinite(rawPort) && rawPort > 0 && rawPort <= 65535
      ? Math.round(rawPort)
      : DEFAULTS.serve.port;

  const host = asString(env.MCP_SERVE_HOST) ?? asString(s.host) ?? DEFAULTS.serve.host;

  return { transport, port, host };
}

/** Load a JSON config file from disk, returning undefined if absent or invalid. */
export function loadConfigFile(path: string | undefined): Record<string, unknown> | undefined {
  if (!path) {
    return undefined;
  }
  try {
    const text = readFileSync(path, "utf8");
    const parsed = JSON.parse(text) as unknown;
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
    return undefined;
  } catch {
    return undefined;
  }
}
