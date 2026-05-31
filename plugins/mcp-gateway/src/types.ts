/**
 * Shared types for the AgentShield MCP gateway.
 *
 * Wire payloads (EvaluationRequest/Response, AuditReport, LifecycleEvent,
 * OverrideRequest) mirror the AgentShield Connector Contract v1.0.0 exactly so
 * that the same Sigma rules apply across every connector. The `source` field is
 * hard-coded to "mcp-gateway".
 */

/** Downstream MCP server transport selection. */
export type DownstreamTransport = "stdio" | "http";

/** Configuration for the downstream MCP server this gateway proxies to. */
export type DownstreamConfig = {
  /** Transport used to reach the downstream server. */
  transport: DownstreamTransport;
  /** Launch command for a stdio downstream server (required when transport=stdio). */
  command: string | null;
  /** Arguments for the stdio launch command. */
  args: string[];
  /** Extra environment variables passed to the stdio child process. */
  env: Record<string, string>;
  /** Base URL for an HTTP/SSE downstream server (required when transport=http). */
  url: string | null;
};

/** How the gateway itself exposes the proxied server to the agent. */
export type ServeConfig = {
  /** Transport the gateway listens on for the upstream agent. */
  transport: DownstreamTransport;
  /** Port for HTTP serving (ignored for stdio). */
  port: number;
  /** Host for HTTP serving (ignored for stdio). */
  host: string;
};

/** Plugin configuration parsed from env + JSON file (contract Section 4). */
export type AgentShieldConfig = {
  enabled: boolean;
  endpoint: string;
  auth_token: string;
  timeout_ms: number;
  timeout_policy: "allow" | "block" | "log";
  intercept: string[];
  skip: string[];
  notify: "all" | "high" | "critical" | "none";
  circuit_breaker: {
    failure_threshold: number;
    recovery_interval_ms: number;
  };
  downstream: DownstreamConfig;
  serve: ServeConfig;
};

/** Evaluation request payload (contract Section 1.1). */
export type EvaluationRequest = {
  event_id: string;
  timestamp: string;
  event_type: string;
  tool_name: string;
  source: "mcp-gateway";
  command: string | null;
  params: Record<string, unknown>;
  agent_id: string | null;
  session_id: string | null;
  working_dir: string | null;
  data: Record<string, unknown>;
};

/** Triage result from LLM analysis. */
export type TriageResult = {
  verdict: "block" | "allow" | "investigate";
  confidence: number;
  reasoning: string;
  suggested_action: string;
  provider: string;
  model: string;
  processing_time: number;
};

/** Alert from the detection engine. */
export type Alert = {
  rule_id: string;
  rule_name: string;
  severity: "low" | "medium" | "high" | "critical";
  description: string;
  matched: boolean;
  matched_fields: Record<string, unknown>;
};

/** Evaluation response payload (contract Section 1.1). */
export type EvaluationResponse = {
  action: "allow" | "block" | "log" | "require_approval";
  event_id: string;
  alerts?: Array<Alert>;
  reason?: string;
  triage_results?: Array<TriageResult>;
  overridable?: boolean;
  effective_mode?: "enforce" | "audit" | "shadow";
  cached?: boolean;
  feedback_url?: string;
  timestamp?: string;
};

/** Audit report payload (contract Section 1.2). */
export type AuditReport = {
  event_id: string;
  correlation_id: string;
  timestamp: string;
  event_type: "tool_result";
  tool_name: string;
  source: "mcp-gateway";
  result_summary: string | null;
  is_error: boolean;
  error_message: string | null;
  duration_ms: number;
  agent_id: string | null;
  session_id: string | null;
  working_dir: string | null;
  data: Record<string, unknown>;
};

/** Lifecycle event payload (contract Section 1.3). */
export type LifecycleEvent = {
  event_id: string;
  timestamp: string;
  event_type: string;
  source: "mcp-gateway";
  agent_id: string | null;
  session_id: string | null;
  data: Record<string, unknown>;
};

/** Override request payload (contract Section 1.5). */
export type OverrideRequest = {
  session_id: string;
  event_id: string;
};

/** Circuit breaker states. */
export type CircuitBreakerState = "closed" | "open" | "half-open";

/** Minimal logger interface. Writes go to stderr to keep stdout clean for stdio MCP. */
export type Logger = {
  debug: (message: string) => void;
  info: (message: string) => void;
  warn: (message: string) => void;
  error: (message: string) => void;
};
