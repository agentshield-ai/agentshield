import type { WeftClient } from './client.js';
import { currentContext } from './context.js';

let globalClient: WeftClient | null = null;

export function setGlobalClient(c: WeftClient | null): void {
  globalClient = c;
}

export function getClient(): WeftClient | null {
  return globalClient;
}

export function requireClient(): WeftClient {
  if (!globalClient) throw new Error("weft is not initialised; call weft.init('<project>') first");
  return globalClient;
}

/** The client to trace with, or null when tracing is off for this context. */
export function tracingClient(): WeftClient | null {
  if (!globalClient || globalClient.disabled || currentContext().suppressed) return null;
  return globalClient;
}
