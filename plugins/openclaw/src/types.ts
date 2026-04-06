/** Plugin configuration parsed from api.pluginConfig. */
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
};

/** Evaluation request payload (contract Section 2.1). */
export type EvaluationRequest = {
  event_id: string;
  timestamp: string;
  event_type: string;
  tool_name: string;
  source: "openclaw";
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

/** Alert with enhanced fields from the new API response. */
export type Alert = {
  rule_id: string;
  rule_name: string;
  severity: "low" | "medium" | "high" | "critical";
  description: string;
  matched: boolean;
  matched_fields: Record<string, unknown>;
};

/** Evaluation response payload (contract Section 2.2). */
export type EvaluationResponse = {
  action: "allow" | "block" | "log" | "require_approval";
  event_id: string;
  alerts?: Array<Alert>;
  reason?: string;
  // New fields in enhanced response format
  triage_results?: Array<TriageResult>;
  overridable?: boolean;
  effective_mode?: "enforce" | "audit" | "shadow";
  feedback_url?: string;
  timestamp?: string;
};

/** Audit report payload (contract Section 2.3). */
export type AuditReport = {
  event_id: string;
  correlation_id: string;
  timestamp: string;
  event_type: "tool_result";
  tool_name: string;
  source: "openclaw";
  result_summary: string | null;
  is_error: boolean;
  error_message: string | null;
  duration_ms: number;
  agent_id: string | null;
  session_id: string | null;
  working_dir: string | null;
  data: Record<string, unknown>;
};

/** Lifecycle event payload (contract Section 15). */
export type LifecycleEvent = {
  event_id: string;
  timestamp: string;
  event_type: string;
  source: "openclaw";
  agent_id: string | null;
  session_id: string | null;
  data: Record<string, unknown>;
};

/** Override request payload sent when user overrides a block/require_approval. */
export type OverrideRequest = {
  session_id: string;
  event_id: string;
};

/** Circuit breaker states. */
export type CircuitBreakerState = "closed" | "open" | "half-open";
