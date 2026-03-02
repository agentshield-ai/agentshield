import type { AgentShieldConfig } from "./types.js";

const DEFAULTS: AgentShieldConfig = {
  enabled: true,
  endpoint: "http://127.0.0.1:8433/api/v1/evaluate",
  auth_token: "",
  timeout_ms: 200,
  timeout_policy: "block",
  intercept: ["exec", "write", "edit", "read", "browser", "message", "sessions_spawn"],
  skip: ["session_status"],
  notify: "high",
  circuit_breaker: {
    failure_threshold: 3,
    recovery_interval_ms: 30_000,
  },
};

const VALID_TIMEOUT_POLICIES = new Set(["allow", "block", "log"]);
const VALID_NOTIFY_LEVELS = new Set(["all", "high", "critical", "none"]);

export function parseConfig(
  raw: Record<string, unknown> | undefined,
): AgentShieldConfig {
  if (!raw || typeof raw !== "object") {
    return { ...DEFAULTS };
  }

  const enabled =
    typeof raw.enabled === "boolean" ? raw.enabled : DEFAULTS.enabled;

  const endpoint =
    typeof raw.endpoint === "string" && raw.endpoint.trim()
      ? raw.endpoint.trim()
      : DEFAULTS.endpoint;

  const auth_token =
    typeof raw.auth_token === "string" ? raw.auth_token : DEFAULTS.auth_token;

  const timeout_ms =
    typeof raw.timeout_ms === "number" &&
    raw.timeout_ms >= 5 &&
    raw.timeout_ms <= 5000
      ? raw.timeout_ms
      : DEFAULTS.timeout_ms;

  const timeout_policy =
    typeof raw.timeout_policy === "string" &&
    VALID_TIMEOUT_POLICIES.has(raw.timeout_policy)
      ? (raw.timeout_policy as AgentShieldConfig["timeout_policy"])
      : DEFAULTS.timeout_policy;

  const intercept = Array.isArray(raw.intercept)
    ? raw.intercept.filter(
        (v): v is string => typeof v === "string" && v.trim().length > 0,
      )
    : DEFAULTS.intercept;

  const notify =
    typeof raw.notify === "string" &&
    VALID_NOTIFY_LEVELS.has(raw.notify)
      ? (raw.notify as AgentShieldConfig["notify"])
      : DEFAULTS.notify;

  const skip = Array.isArray(raw.skip)
    ? raw.skip.filter(
        (v): v is string => typeof v === "string" && v.trim().length > 0,
      )
    : DEFAULTS.skip;

  const rawCb =
    raw.circuit_breaker &&
    typeof raw.circuit_breaker === "object" &&
    !Array.isArray(raw.circuit_breaker)
      ? (raw.circuit_breaker as Record<string, unknown>)
      : {};

  const circuit_breaker = {
    failure_threshold:
      typeof rawCb.failure_threshold === "number" &&
      rawCb.failure_threshold >= 1
        ? rawCb.failure_threshold
        : DEFAULTS.circuit_breaker.failure_threshold,
    recovery_interval_ms:
      typeof rawCb.recovery_interval_ms === "number" &&
      rawCb.recovery_interval_ms >= 1000
        ? rawCb.recovery_interval_ms
        : DEFAULTS.circuit_breaker.recovery_interval_ms,
  };

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
  };
}
