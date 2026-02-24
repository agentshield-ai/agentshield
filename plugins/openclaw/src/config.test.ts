import { describe, expect, it } from "vitest";

import { parseConfig } from "./config.js";

describe("parseConfig", () => {
  it("returns defaults when given undefined", () => {
    const config = parseConfig(undefined);
    expect(config.enabled).toBe(true);
    expect(config.endpoint).toBe("http://127.0.0.1:8433/api/v1/evaluate");
    expect(config.auth_token).toBe("");
    expect(config.timeout_ms).toBe(200);
    expect(config.timeout_policy).toBe("block");
    expect(config.intercept).toEqual([
      "exec",
      "write",
      "edit",
      "browser",
      "message",
      "sessions_spawn",
    ]);
    expect(config.skip).toEqual(["read", "session_status"]);
    expect(config.circuit_breaker.failure_threshold).toBe(3);
    expect(config.circuit_breaker.recovery_interval_ms).toBe(30_000);
  });

  it("returns defaults when given empty object", () => {
    const config = parseConfig({});
    expect(config.enabled).toBe(true);
    expect(config.timeout_ms).toBe(200);
  });

  it("parses provided values", () => {
    const config = parseConfig({
      enabled: false,
      endpoint: "http://localhost:9999/api/v1/evaluate",
      auth_token: "secret123",
      timeout_ms: 100,
      timeout_policy: "block",
      intercept: ["exec"],
      skip: [],
      circuit_breaker: {
        failure_threshold: 5,
        recovery_interval_ms: 60_000,
      },
    });

    expect(config.enabled).toBe(false);
    expect(config.endpoint).toBe("http://localhost:9999/api/v1/evaluate");
    expect(config.auth_token).toBe("secret123");
    expect(config.timeout_ms).toBe(100);
    expect(config.timeout_policy).toBe("block");
    expect(config.intercept).toEqual(["exec"]);
    expect(config.skip).toEqual([]);
    expect(config.circuit_breaker.failure_threshold).toBe(5);
    expect(config.circuit_breaker.recovery_interval_ms).toBe(60_000);
  });

  it("rejects invalid timeout_policy and uses default", () => {
    const config = parseConfig({ timeout_policy: "invalid" });
    expect(config.timeout_policy).toBe("block");
  });

  it("clamps timeout_ms to valid range", () => {
    expect(parseConfig({ timeout_ms: 1 }).timeout_ms).toBe(200); // Below min
    expect(parseConfig({ timeout_ms: 10000 }).timeout_ms).toBe(200); // Above max
    expect(parseConfig({ timeout_ms: 200 }).timeout_ms).toBe(200); // Valid
  });

  it("filters non-string entries from intercept and skip arrays", () => {
    const config = parseConfig({
      intercept: ["exec", 42, "", null, "write"],
      skip: [true, "read"],
    });
    expect(config.intercept).toEqual(["exec", "write"]);
    expect(config.skip).toEqual(["read"]);
  });

  it("uses default circuit breaker values for invalid input", () => {
    const config = parseConfig({
      circuit_breaker: {
        failure_threshold: 0, // Below minimum
        recovery_interval_ms: 500, // Below minimum
      },
    });
    expect(config.circuit_breaker.failure_threshold).toBe(3);
    expect(config.circuit_breaker.recovery_interval_ms).toBe(30_000);
  });
});
