import type {
  AgentShieldConfig,
  AuditReport,
  EvaluationRequest,
  EvaluationResponse,
  LifecycleEvent,
  Logger,
  OverrideRequest,
} from "./types.js";

const CONTRACT_VERSION = "1.0.0";

const VALID_ACTIONS = new Set(["allow", "block", "log", "require_approval"]);

const HEALTH_TIMEOUT_MS = 2000;

/**
 * HTTP client for the AgentShield engine API (contract Section 1).
 *
 * - `evaluate()` is synchronous (awaited) with a configurable timeout.
 * - `sendAudit()`, `sendLifecycle()`, `submitFeedback()`, `sendOverride()` are
 *   fire-and-forget; failures are swallowed at debug level.
 * - The evaluate endpoint is configured; the others are derived by stripping
 *   the trailing `/evaluate` (Section 1).
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

  /** Synchronous, awaited, timeout-bounded evaluation (contract Section 1.1). */
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

    if (typeof body.action !== "string" || !VALID_ACTIONS.has(body.action)) {
      throw new Error(`AgentShield returned invalid action: ${String(body.action)}`);
    }

    return body as unknown as EvaluationResponse;
  }

  /** Fire-and-forget audit report after tool execution (Section 1.2). */
  sendAudit(report: AuditReport): void {
    this.fireAndForget(this.auditEndpoint, report, "audit");
  }

  /** Fire-and-forget lifecycle event (Section 1.3). */
  sendLifecycle(event: LifecycleEvent): void {
    this.fireAndForget(this.lifecycleEndpoint, event, "lifecycle");
  }

  /** Non-blocking startup probe; treats HTTP 2xx as reachable (Section 1.4). */
  async healthCheck(): Promise<boolean> {
    try {
      const response = await fetch(this.healthEndpoint, {
        method: "GET",
        headers: this.buildHeaders(),
        signal: AbortSignal.timeout(HEALTH_TIMEOUT_MS),
      });
      return response.ok;
    } catch {
      return false;
    }
  }

  /** Fire-and-forget feedback for an evaluation event (Section 1.6). */
  submitFeedback(eventId: string, feedbackType: string, comment?: string): void {
    const payload = {
      event_id: eventId,
      feedback_type: feedbackType,
      comment: comment ?? null,
      timestamp: new Date().toISOString(),
    };
    this.fireAndForget(this.feedbackEndpoint, payload, "feedback");
  }

  /** Fire-and-forget override report (Section 1.5). */
  sendOverride(sessionId: string, eventId: string): void {
    const payload: OverrideRequest = { session_id: sessionId, event_id: eventId };
    this.fireAndForget(this.overrideEndpoint, payload, "override");
  }

  private fireAndForget(url: string, payload: unknown, label: string): void {
    fetch(url, {
      method: "POST",
      headers: this.buildHeaders(),
      body: JSON.stringify(payload),
    }).catch((err: unknown) => {
      this.logger.debug(`AgentShield ${label} send failed: ${String(err)}`);
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
