import { describe, expect, it, vi } from "vitest";

import type { AgentShieldClient } from "./client.js";
import { GatewayPipeline } from "./pipeline.js";
import type { AgentShieldConfig, EvaluationResponse, Logger } from "./types.js";

const SILENT_LOGGER: Logger = {
  debug: () => undefined,
  info: () => undefined,
  warn: () => undefined,
  error: () => undefined,
};

const CTX = { agentId: "a", sessionId: "s" };

function baseConfig(overrides: Partial<AgentShieldConfig> = {}): AgentShieldConfig {
  return {
    enabled: true,
    endpoint: "http://127.0.0.1:8433/api/v1/evaluate",
    auth_token: "",
    timeout_ms: 200,
    timeout_policy: "block",
    intercept: [],
    skip: ["todo"],
    notify: "high",
    circuit_breaker: { failure_threshold: 1, recovery_interval_ms: 30_000 },
    downstream: { transport: "stdio", command: "x", args: [], env: {}, url: null },
    serve: { transport: "stdio", port: 8434, host: "127.0.0.1" },
    ...overrides,
  };
}

/** Build a pipeline with a stubbed client returning the given evaluate result. */
function makePipeline(
  evaluate: () => Promise<EvaluationResponse> | EvaluationResponse,
  config: AgentShieldConfig = baseConfig(),
): { pipeline: GatewayPipeline; sendAudit: ReturnType<typeof vi.fn> } {
  const sendAudit = vi.fn();
  const client = {
    evaluate: vi.fn(async () => evaluate()),
    sendAudit,
    sendLifecycle: vi.fn(),
    healthCheck: vi.fn(async () => true),
    submitFeedback: vi.fn(),
    sendOverride: vi.fn(),
  } as unknown as AgentShieldClient;
  return { pipeline: new GatewayPipeline(config, client, SILENT_LOGGER), sendAudit };
}

const ALLOW: EvaluationResponse = { action: "allow", event_id: "e" };

describe("GatewayPipeline.evaluateToolCall", () => {
  it("allows when the engine returns allow", async () => {
    const { pipeline } = makePipeline(() => ALLOW);
    const d = await pipeline.evaluateToolCall("exec", { command: "ls" }, CTX);
    expect(d.allowed).toBe(true);
  });

  it("blocks when the engine returns block", async () => {
    const { pipeline } = makePipeline(() => ({
      action: "block",
      event_id: "e",
      reason: "dangerous",
    }));
    const d = await pipeline.evaluateToolCall("exec", { command: "rm -rf /" }, CTX);
    expect(d.allowed).toBe(false);
    expect(d.reason).toBe("dangerous");
  });

  it("appends triage reasoning to block reasons", async () => {
    const { pipeline } = makePipeline(() => ({
      action: "block",
      event_id: "e",
      reason: "blocked",
      triage_results: [
        {
          verdict: "block",
          confidence: 0.9,
          reasoning: "rm of root",
          suggested_action: "stop",
          provider: "openai",
          model: "gpt",
          processing_time: 1,
        },
      ],
    }));
    const d = await pipeline.evaluateToolCall("exec", { command: "rm -rf /" }, CTX);
    expect(d.reason).toBe("blocked (Triage: rm of root)");
  });

  it("fails closed (blocks) on require_approval", async () => {
    const { pipeline } = makePipeline(() => ({
      action: "require_approval",
      event_id: "e",
      overridable: true,
    }));
    const d = await pipeline.evaluateToolCall("exec", { command: "sudo x" }, CTX);
    expect(d.allowed).toBe(false);
    expect(d.overridable).toBe(true);
  });

  it("allows on the log action", async () => {
    const { pipeline } = makePipeline(() => ({
      action: "log",
      event_id: "e",
      alerts: [
        {
          rule_id: "r",
          rule_name: "n",
          severity: "low",
          description: "d",
          matched: true,
          matched_fields: {},
        },
      ],
    }));
    const d = await pipeline.evaluateToolCall("exec", { command: "ls" }, CTX);
    expect(d.allowed).toBe(true);
  });

  it("skips tools in the skip set without evaluating", async () => {
    const evaluate = vi.fn(() => ALLOW);
    const { pipeline } = makePipeline(evaluate, baseConfig({ skip: ["todo"] }));
    const d = await pipeline.evaluateToolCall("todo", {}, CTX);
    expect(d.allowed).toBe(true);
    expect(evaluate).not.toHaveBeenCalled();
  });

  it("skips tools whose canonical name is in the skip set", async () => {
    const evaluate = vi.fn(() => ALLOW);
    const { pipeline } = makePipeline(evaluate, baseConfig({ skip: ["planning"] }));
    const d = await pipeline.evaluateToolCall("task_plan", {}, CTX);
    expect(d.allowed).toBe(true);
    expect(evaluate).not.toHaveBeenCalled();
  });

  it("allows non-intercepted tools without evaluating when intercept is set", async () => {
    const evaluate = vi.fn(() => ALLOW);
    const { pipeline } = makePipeline(evaluate, baseConfig({ intercept: ["exec"] }));
    const d = await pipeline.evaluateToolCall("read_file", { path: "/x" }, CTX);
    expect(d.allowed).toBe(true);
    expect(evaluate).not.toHaveBeenCalled();
  });

  it("evaluates intercepted tools matched by canonical name", async () => {
    const evaluate = vi.fn(() => ALLOW);
    const { pipeline } = makePipeline(evaluate, baseConfig({ intercept: ["read"] }));
    await pipeline.evaluateToolCall("read_file", { path: "/x" }, CTX);
    expect(evaluate).toHaveBeenCalledOnce();
  });

  it("fails closed by default when evaluation throws", async () => {
    const { pipeline } = makePipeline(() => {
      throw new Error("network down");
    });
    const d = await pipeline.evaluateToolCall("exec", { command: "ls" }, CTX);
    expect(d.allowed).toBe(false);
    expect(d.reason).toContain("fail-closed");
  });

  it("fails open when timeout_policy is allow", async () => {
    const { pipeline } = makePipeline(
      () => {
        throw new Error("network down");
      },
      baseConfig({ timeout_policy: "allow" }),
    );
    const d = await pipeline.evaluateToolCall("exec", { command: "ls" }, CTX);
    expect(d.allowed).toBe(true);
  });

  it("opens the circuit breaker and blocks after the failure threshold", async () => {
    const { pipeline } = makePipeline(
      () => {
        throw new Error("down");
      },
      baseConfig({ timeout_policy: "block", circuit_breaker: { failure_threshold: 1, recovery_interval_ms: 30_000 } }),
    );
    // First call fails and opens the breaker.
    await pipeline.evaluateToolCall("exec", { command: "ls" }, CTX);
    // Second call should short-circuit via the breaker (still blocked).
    const d = await pipeline.evaluateToolCall("exec", { command: "ls" }, CTX);
    expect(d.allowed).toBe(false);
  });
});

describe("GatewayPipeline.recordResult", () => {
  it("sends a fire-and-forget audit report", () => {
    const { pipeline, sendAudit } = makePipeline(() => ALLOW);
    pipeline.recordResult("exec", { command: "ls" }, CTX, "out", {
      isError: false,
      errorMessage: null,
      durationMs: 5,
    });
    expect(sendAudit).toHaveBeenCalledOnce();
    const report = sendAudit.mock.calls[0]?.[0];
    expect(report.event_type).toBe("tool_result");
    expect(report.tool_name).toBe("exec");
  });
});
