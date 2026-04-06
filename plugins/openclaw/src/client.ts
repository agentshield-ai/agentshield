import type {
  AgentShieldConfig,
  AuditReport,
  EvaluationRequest,
  EvaluationResponse,
  LifecycleEvent,
  OverrideRequest,
} from "./types.js";

type Logger = {
  debug?: (message: string) => void;
  info: (message: string) => void;
  warn: (message: string) => void;
  error: (message: string) => void;
};

const CONTRACT_VERSION = "1.0.0";

const VALID_ACTIONS = new Set(["allow", "block", "log", "require_approval"]);

/**
 * HTTP client for communicating with the AgentShield real-time receiver.
 *
 * - `evaluate()` is synchronous (awaited) with a configurable timeout.
 * - `sendAudit()` and `sendLifecycle()` are fire-and-forget.
 * - `submitFeedback()` sends feedback for an evaluation event.
 */
export class AgentShieldClient {
  private readonly endpoint: string;
  private readonly auditEndpoint: string;
  private readonly lifecycleEndpoint: string;
  private readonly healthEndpoint: string;
  private readonly feedbackEndpoint: string;
  private readonly overrideEndpoint: string;
  private readonly timeoutMs: number;
  private readonly authToken: string;
  private readonly logger: Logger;

  constructor(config: AgentShieldConfig, logger: Logger) {
    this.endpoint = config.endpoint;
    const baseUrl = config.endpoint.replace(/\/evaluate$/, "");
    this.auditEndpoint = `${baseUrl}/audit`;
    this.lifecycleEndpoint = `${baseUrl}/lifecycle`;
    this.healthEndpoint = `${baseUrl}/health`;
    this.feedbackEndpoint = `${baseUrl}/feedback`;
    this.overrideEndpoint = `${baseUrl}/override`;
    this.timeoutMs = config.timeout_ms;
    this.authToken = config.auth_token;
    this.logger = logger;
  }

  async evaluate(request: EvaluationRequest): Promise<EvaluationResponse> {
    const response = await fetch(this.endpoint, {
      method: "POST",
      headers: this.buildHeaders(),
      body: JSON.stringify(request),
      signal: AbortSignal.timeout(this.timeoutMs),
    });

    if (!response.ok) {
      throw new Error(
        `AgentShield returned HTTP ${response.status}: ${response.statusText}`,
      );
    }

    const body = (await response.json()) as Record<string, unknown>;

    if (
      typeof body.action !== "string" ||
      !VALID_ACTIONS.has(body.action)
    ) {
      throw new Error(
        `AgentShield returned invalid action: ${String(body.action)}`,
      );
    }

    return body as unknown as EvaluationResponse;
  }

  /** Fire-and-forget audit report after tool execution. */
  sendAudit(report: AuditReport): void {
    this.fireAndForget(this.auditEndpoint, report, "audit");
  }

  /** Fire-and-forget lifecycle event. */
  sendLifecycle(event: LifecycleEvent): void {
    this.fireAndForget(this.lifecycleEndpoint, event, "lifecycle");
  }

  /** Check whether AgentShield is reachable. */
  async healthCheck(): Promise<boolean> {
    try {
      const response = await fetch(this.healthEndpoint, {
        method: "GET",
        headers: this.buildHeaders(),
        signal: AbortSignal.timeout(2000),
      });
      return response.ok;
    } catch {
      return false;
    }
  }

  /** Submit feedback for an evaluation event. */
  submitFeedback(
    eventId: string,
    feedbackType: string,
    comment?: string,
  ): void {
    const payload = {
      event_id: eventId,
      feedback_type: feedbackType,
      comment: comment ?? null,
      timestamp: new Date().toISOString(),
    };

    this.fireAndForget(this.feedbackEndpoint, payload, "feedback");
  }

  /**
   * Report that a user overrode a block or require_approval verdict.
   * Fire-and-forget — the engine updates session.override_count for
   * downstream Sigma rules (ai_agent_override_escalation.yml).
   */
  sendOverride(sessionId: string, eventId: string): void {
    const payload: OverrideRequest = {
      session_id: sessionId,
      event_id: eventId,
    };

    this.fireAndForget(this.overrideEndpoint, payload, "override");
  }

  /** POST payload to url, swallowing errors at debug level. */
  private fireAndForget(url: string, payload: unknown, label: string): void {
    fetch(url, {
      method: "POST",
      headers: this.buildHeaders(),
      body: JSON.stringify(payload),
    }).catch((err) => {
      this.logger.debug?.(
        `AgentShield ${label} send failed: ${String(err)}`,
      );
    });
  }

  private buildHeaders(): Record<string, string> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json; charset=utf-8",
      "X-AgentShield-Version": CONTRACT_VERSION,
    };
    if (this.authToken) {
      headers["Authorization"] = `Bearer ${this.authToken}`;
    }
    return headers;
  }
}
