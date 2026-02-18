import type { CircuitBreakerState } from "./types.js";

export type CircuitBreakerOptions = {
  failureThreshold: number;
  recoveryIntervalMs: number;
};

/**
 * Simple three-state circuit breaker for the AgentShield HTTP client.
 *
 * State machine:
 *   closed  --(N consecutive failures)--> open
 *   open    --(recoveryIntervalMs elapsed)--> half-open
 *   half-open --(success)--> closed
 *   half-open --(failure)--> open
 */
export class CircuitBreaker {
  private state: CircuitBreakerState = "closed";
  private consecutiveFailures = 0;
  private lastFailureTime = 0;
  private readonly failureThreshold: number;
  private readonly recoveryIntervalMs: number;

  constructor(opts: CircuitBreakerOptions) {
    this.failureThreshold = opts.failureThreshold;
    this.recoveryIntervalMs = opts.recoveryIntervalMs;
  }

  /** Returns true if the circuit is open and calls should be skipped. */
  isOpen(): boolean {
    if (this.state === "closed") {
      return false;
    }

    if (this.state === "open") {
      const elapsed = Date.now() - this.lastFailureTime;
      if (elapsed >= this.recoveryIntervalMs) {
        this.state = "half-open";
        return false; // Allow a probe request
      }
      return true;
    }

    // half-open: allow the probe request through
    return false;
  }

  recordSuccess(): void {
    this.consecutiveFailures = 0;
    this.state = "closed";
  }

  recordFailure(): void {
    this.consecutiveFailures += 1;
    this.lastFailureTime = Date.now();

    if (this.state === "half-open") {
      this.state = "open";
      return;
    }

    if (this.consecutiveFailures >= this.failureThreshold) {
      this.state = "open";
    }
  }

  getState(): CircuitBreakerState {
    // Re-evaluate open -> half-open transition on read
    if (this.state === "open") {
      const elapsed = Date.now() - this.lastFailureTime;
      if (elapsed >= this.recoveryIntervalMs) {
        this.state = "half-open";
      }
    }
    return this.state;
  }
}
