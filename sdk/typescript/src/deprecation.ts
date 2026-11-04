const warned = new Set<string>();

/** Warn once per process that `oldName` is deprecated (CHANGELOG.md lists the earlier names). */
export function deprecated(oldName: string, newName: string): void {
  if (warned.has(oldName)) return;
  warned.add(oldName);
  // eslint-disable-next-line no-console
  console.warn(`weft: ${oldName} is deprecated; use ${newName}`);
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
