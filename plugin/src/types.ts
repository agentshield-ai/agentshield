/** Plugin configuration parsed from api.pluginConfig. */
export type AgentShieldConfig = {
  enabled: boolean;
  endpoint: string;
  auth_token: string;
  timeout_ms: number;
  timeout_policy: "allow" | "block" | "log";
  intercept: string[];
  skip: string[];
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

/** Evaluation response payload (contract Section 2.2). */
export type EvaluationResponse = {
  action: "allow" | "block" | "log";
  event_id: string;
  alerts?: Array<{
    rule_id: string;
    rule_name: string;
    level: "low" | "medium" | "high" | "critical";
    description?: string;
  }>;
  reason?: string;
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

/** Circuit breaker states. */
export type CircuitBreakerState = "closed" | "open" | "half-open";
