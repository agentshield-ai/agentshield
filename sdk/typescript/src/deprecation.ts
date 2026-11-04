const warned = new Set<string>();

/** The `code` of the SDK's deprecation warnings (`process.on('warning')` sees `warning.code`). */
export const DEPRECATION_CODE = 'WEFT_DEPRECATED';

/**
 * Warn once per process that `oldName` is deprecated (CHANGELOG.md lists the earlier names).
 * In Node this is a `DeprecationWarning` (`process.emitWarning`, code `WEFT_DEPRECATED`), so
 * `process.on('warning')`, `--no-deprecation` and `--throw-deprecation` apply; elsewhere
 * (browsers) it is a `console.warn`.
 */
export function deprecated(oldName: string, newName: string): void {
  if (warned.has(oldName)) return;
  warned.add(oldName);
  const msg = `weft: ${oldName} is deprecated; use ${newName}`;
  const proc = (globalThis as { process?: { emitWarning?: (m: string, o: { type: string; code: string }) => void } }).process;
  if (typeof proc?.emitWarning === 'function') {
    proc.emitWarning(msg, { type: 'DeprecationWarning', code: DEPRECATION_CODE });
    return;
  }
  // eslint-disable-next-line no-console
  console.warn(msg);
}

/** @internal For tests: forget which warnings were shown. */
export function resetDeprecationWarnings(): void {
  warned.clear();
}

/** A deprecated alias of a class: works like the class and warns once when used. */
export function deprecatedClass<T extends abstract new (...args: any[]) => any>(cls: T, oldName: string, newName: string): T {
  return new Proxy(cls, {
    construct(target, args, newTarget) {
      deprecated(oldName, newName);
      return Reflect.construct(target, args, newTarget === (undefined as unknown) ? target : newTarget);
    },
    get(target, prop, receiver) {
      if (prop !== 'prototype') deprecated(oldName, newName);
      return Reflect.get(target, prop, receiver);
    },
  });
}
