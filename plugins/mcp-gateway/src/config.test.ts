import { describe, expect, it } from "vitest";

import { parseConfig } from "./config.js";

const EMPTY_ENV = {} as NodeJS.ProcessEnv;

describe("parseConfig", () => {
  it("returns defaults when given undefined", () => {
    const config = parseConfig(undefined, EMPTY_ENV);
    expect(config.enabled).toBe(true);
    expect(config.endpoint).toBe("http://127.0.0.1:8433/api/v1/evaluate");
    expect(config.auth_token).toBe("");
    expect(config.timeout_ms).toBe(200);
    expect(config.timeout_policy).toBe("block");
    expect(config.notify).toBe("high");
    expect(config.circuit_breaker.failure_threshold).toBe(3);
    expect(config.circuit_breaker.recovery_interval_ms).toBe(30_000);
    expect(config.downstream.transport).toBe("stdio");
    expect(config.serve.transport).toBe("stdio");
  });

  it("parses provided values", () => {
    const config = parseConfig(
      {
        enabled: false,
        endpoint: "http://localhost:9999/api/v1/evaluate",
        auth_token: "secret123",
        timeout_ms: 100,
        timeout_policy: "log",
        intercept: ["exec"],
        skip: [],
        notify: "all",
        circuit_breaker: { failure_threshold: 5, recovery_interval_ms: 60_000 },
        downstream: { transport: "http", url: "http://localhost:7000/mcp" },
      },
      EMPTY_ENV,
    );
    expect(config.enabled).toBe(false);
    expect(config.endpoint).toBe("http://localhost:9999/api/v1/evaluate");
    expect(config.auth_token).toBe("secret123");
    expect(config.timeout_ms).toBe(100);
    expect(config.timeout_policy).toBe("log");
    expect(config.intercept).toEqual(["exec"]);
    expect(config.skip).toEqual([]);
    expect(config.circuit_breaker.failure_threshold).toBe(5);
    expect(config.downstream.transport).toBe("http");
    expect(config.downstream.url).toBe("http://localhost:7000/mcp");
  });

  it("falls back to AGENTSHIELD_AUTH_TOKEN env var when no token configured", () => {
    const config = parseConfig({}, { AGENTSHIELD_AUTH_TOKEN: "env-token" } as NodeJS.ProcessEnv);
    expect(config.auth_token).toBe("env-token");
  });

  it("prefers explicit config token over the env var", () => {
    const config = parseConfig(
      { auth_token: "cfg-token" },
      { AGENTSHIELD_AUTH_TOKEN: "env-token" } as NodeJS.ProcessEnv,
    );
    expect(config.auth_token).toBe("cfg-token");
  });

  it("rejects invalid timeout_policy and uses default", () => {
    expect(parseConfig({ timeout_policy: "invalid" }, EMPTY_ENV).timeout_policy).toBe("block");
  });

  it("clamps timeout_ms to the valid range", () => {
    expect(parseConfig({ timeout_ms: 1 }, EMPTY_ENV).timeout_ms).toBe(200);
    expect(parseConfig({ timeout_ms: 10_000 }, EMPTY_ENV).timeout_ms).toBe(200);
    expect(parseConfig({ timeout_ms: 250 }, EMPTY_ENV).timeout_ms).toBe(250);
  });

  it("filters non-string entries from intercept and skip arrays", () => {
    const config = parseConfig(
      { intercept: ["exec", 42, "", null, "write"], skip: [true, "read"] },
      EMPTY_ENV,
    );
    expect(config.intercept).toEqual(["exec", "write"]);
    expect(config.skip).toEqual(["read"]);
  });

  it("uses default circuit breaker values for invalid input", () => {
    const config = parseConfig(
      { circuit_breaker: { failure_threshold: 0, recovery_interval_ms: 500 } },
      EMPTY_ENV,
    );
    expect(config.circuit_breaker.failure_threshold).toBe(3);
    expect(config.circuit_breaker.recovery_interval_ms).toBe(30_000);
  });

  it("honours environment overrides for endpoint and timeout", () => {
    const config = parseConfig(
      {},
      {
        AGENTSHIELD_ENDPOINT: "http://host:1234/api/v1/evaluate",
        AGENTSHIELD_TIMEOUT_MS: "300",
        AGENTSHIELD_TIMEOUT_POLICY: "allow",
      } as NodeJS.ProcessEnv,
    );
    expect(config.endpoint).toBe("http://host:1234/api/v1/evaluate");
    expect(config.timeout_ms).toBe(300);
    expect(config.timeout_policy).toBe("allow");
  });

  it("reads downstream stdio command from env", () => {
    const config = parseConfig(
      {},
      {
        MCP_DOWNSTREAM_COMMAND: "npx",
        MCP_DOWNSTREAM_ARGS: "-y @modelcontextprotocol/server-filesystem /data",
      } as NodeJS.ProcessEnv,
    );
    expect(config.downstream.command).toBe("npx");
    expect(config.downstream.args).toEqual([
      "-y",
      "@modelcontextprotocol/server-filesystem",
      "/data",
    ]);
  });
});
