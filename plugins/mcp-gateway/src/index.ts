import type { Logger } from "./types.js";

export { parseConfig, loadConfigFile } from "./config.js";
export { CircuitBreaker } from "./circuit-breaker.js";
export { AgentShieldClient } from "./client.js";
export {
  normaliseToolCall,
  normaliseToolName,
  eventTypeForToolCall,
  stripNamespace,
} from "./normalise.js";
export {
  buildEvaluationRequest,
  buildAuditReport,
  buildLifecycleEvent,
  summariseResult,
} from "./event-builder.js";
export { GatewayPipeline, type Decision } from "./pipeline.js";
export { McpGateway } from "./gateway.js";
export type * from "./types.js";

/**
 * Build a stderr-only logger.
 *
 * stdout must stay reserved for the MCP stdio framing, so every diagnostic is
 * written to stderr. The level gates debug output. Logger name is "agentshield"
 * to match the reference connectors (contract Section 6).
 */
export function createLogger(level: "debug" | "info" | "warn" | "error" = "info"): Logger {
  const order: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 };
  const threshold = order[level] ?? 1;
  const emit = (lvl: keyof typeof order, message: string): void => {
    if ((order[lvl] ?? 1) >= threshold) {
      process.stderr.write(`[agentshield] ${lvl.toUpperCase()} ${message}\n`);
    }
  };
  return {
    debug: (m) => emit("debug", m),
    info: (m) => emit("info", m),
    warn: (m) => emit("warn", m),
    error: (m) => emit("error", m),
  };
}
