/**
 * The SDK's logger. `WEFT_LOG_LEVEL` (or `settings.logLevel`, or
 * `setLogLevel`) picks the lowest level printed: `debug`, `info`, `warning`
 * (default), `error` or `critical`, like Weave's `WEAVE_LOG_LEVEL`. Messages go
 * to stderr through `console`.
 */
export type LogLevel = 'debug' | 'info' | 'warning' | 'error' | 'critical';

const ORDER: Record<LogLevel, number> = { debug: 10, info: 20, warning: 30, error: 40, critical: 50 };

/** Parse a level name (case-insensitive; `warn` = `warning`); undefined when unknown. */
export function parseLogLevel(v: unknown): LogLevel | undefined {
  if (typeof v !== 'string' || !v.trim()) return undefined;
  const s = v.trim().toLowerCase();
  const l = (s === 'warn' ? 'warning' : s) as LogLevel;
  return l in ORDER ? l : undefined;
}

let current: LogLevel = parseLogLevel(process.env.WEFT_LOG_LEVEL) ?? 'warning';

export function setLogLevel(level: LogLevel | string): LogLevel {
  const l = parseLogLevel(level);
  if (!l) throw new Error(`unknown log level ${JSON.stringify(level)} (debug, info, warning, error, critical)`);
  current = l;
  return l;
}

export function getLogLevel(): LogLevel {
  return current;
}

function enabled(l: LogLevel): boolean {
  return ORDER[l] >= ORDER[current];
}

export const logger = {
  debug: (...a: unknown[]): void => void (enabled('debug') && console.debug(...a)),
  info: (...a: unknown[]): void => void (enabled('info') && console.info(...a)),
  warn: (...a: unknown[]): void => void (enabled('warning') && console.warn(...a)),
  error: (...a: unknown[]): void => void (enabled('error') && console.error(...a)),
};
