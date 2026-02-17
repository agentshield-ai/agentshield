import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CircuitBreaker } from "./circuit-breaker.js";

describe("CircuitBreaker", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("starts in closed state", () => {
    const cb = new CircuitBreaker({
      failureThreshold: 3,
      recoveryIntervalMs: 30_000,
    });
    expect(cb.getState()).toBe("closed");
    expect(cb.isOpen()).toBe(false);
  });

  it("transitions to open after threshold failures", () => {
    const cb = new CircuitBreaker({
      failureThreshold: 3,
      recoveryIntervalMs: 30_000,
    });

    cb.recordFailure();
    expect(cb.getState()).toBe("closed");

    cb.recordFailure();
    expect(cb.getState()).toBe("closed");

    cb.recordFailure();
    expect(cb.getState()).toBe("open");
    expect(cb.isOpen()).toBe(true);
  });

  it("resets failure count on success", () => {
    const cb = new CircuitBreaker({
      failureThreshold: 3,
      recoveryIntervalMs: 30_000,
    });

    cb.recordFailure();
    cb.recordFailure();
    cb.recordSuccess();

    // Count should be reset, need 3 more to trip
    cb.recordFailure();
    cb.recordFailure();
    expect(cb.getState()).toBe("closed");

    cb.recordFailure();
    expect(cb.getState()).toBe("open");
  });

  it("transitions from open to half-open after recovery interval", () => {
    const cb = new CircuitBreaker({
      failureThreshold: 1,
      recoveryIntervalMs: 30_000,
    });

    cb.recordFailure();
    expect(cb.getState()).toBe("open");
    expect(cb.isOpen()).toBe(true);

    // Advance time past recovery interval
    vi.advanceTimersByTime(30_001);

    expect(cb.isOpen()).toBe(false); // Allows probe
    expect(cb.getState()).toBe("half-open");
  });

  it("transitions from half-open to closed on success", () => {
    const cb = new CircuitBreaker({
      failureThreshold: 1,
      recoveryIntervalMs: 30_000,
    });

    cb.recordFailure();
    vi.advanceTimersByTime(30_001);

    // Now half-open
    expect(cb.isOpen()).toBe(false);

    cb.recordSuccess();
    expect(cb.getState()).toBe("closed");
  });

  it("transitions from half-open to open on failure", () => {
    const cb = new CircuitBreaker({
      failureThreshold: 1,
      recoveryIntervalMs: 30_000,
    });

    cb.recordFailure();
    vi.advanceTimersByTime(30_001);

    // Now half-open, allow probe
    expect(cb.isOpen()).toBe(false);

    // Probe fails
    cb.recordFailure();
    expect(cb.getState()).toBe("open");
    expect(cb.isOpen()).toBe(true);
  });

  it("remains open before recovery interval elapses", () => {
    const cb = new CircuitBreaker({
      failureThreshold: 1,
      recoveryIntervalMs: 30_000,
    });

    cb.recordFailure();
    expect(cb.isOpen()).toBe(true);

    vi.advanceTimersByTime(15_000); // Half the interval
    expect(cb.isOpen()).toBe(true);
  });
});
