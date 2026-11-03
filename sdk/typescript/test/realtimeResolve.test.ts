/**
 * `patchRealtimeSession()` without an argument finds `@openai/agents-realtime`
 * from the application's location (the working directory), not only from the
 * SDK's, and patches both builds of a dual ESM/CommonJS package.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { afterEach, describe, expect, it, vi } from 'vitest';
import * as weft from '../src/index.js';
import { resolveEsmEntry } from '../src/integrations/openai_realtime.js';

const SESSION_CLASS = `class RealtimeSession {
  constructor(agent, options) { this.agent = agent; this.options = options; this.listeners = {}; }
  on(event, fn) { (this.listeners[event] ??= []).push(fn); return this; }
  async connect() { this.connected = true; }
  close() { this.connected = false; }
}`;

/** An application directory with a dual package in node_modules (like the real one: exports with import/require). */
function makeApp(): string {
  const app = mkdtempSync(join(tmpdir(), 'weft-rt-app-'));
  const pkg = join(app, 'node_modules', '@openai', 'agents-realtime');
  mkdirSync(join(pkg, 'dist'), { recursive: true });
  writeFileSync(
    join(pkg, 'package.json'),
    JSON.stringify({
      name: '@openai/agents-realtime',
      version: '0.21.0',
      main: 'dist/index.js',
      exports: {
        '.': {
          browser: { types: './dist/index.d.ts', require: './dist/index.js', import: './dist/index.mjs' },
          types: './dist/index.d.ts',
          require: './dist/index.js',
          import: './dist/index.mjs',
        },
        './testing': { require: './dist/testing.js', import: './dist/testing.mjs' },
      },
    }),
  );
  writeFileSync(join(pkg, 'dist', 'index.mjs'), `export ${SESSION_CLASS}\nexport const build = 'esm';\n`);
  writeFileSync(join(pkg, 'dist', 'index.js'), `${SESSION_CLASS}\nmodule.exports = { RealtimeSession, build: 'cjs' };\n`);
  mkdirSync(join(app, 'src', 'nested'), { recursive: true });
  return app;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('patchRealtimeSession() module resolution', () => {
  it('resolves the ESM entry the way an import from the application would', () => {
    const app = makeApp();
    expect(resolveEsmEntry('@openai/agents-realtime', join(app, 'src', 'nested'))).toBe(join(app, 'node_modules', '@openai', 'agents-realtime', 'dist', 'index.mjs'));
    expect(resolveEsmEntry('@openai/agents-realtime', tmpdir())).toBeUndefined();
  });

  it('finds the package from the working directory and patches its ESM and CommonJS builds', async () => {
    // Not resolvable from the SDK's own location.
    await expect(import('@openai/agents-realtime' as string)).rejects.toThrow();
    const app = makeApp();
    vi.spyOn(process, 'cwd').mockReturnValue(join(app, 'src'));
    expect(await weft.patchRealtimeSession()).toBe(true);
    const esm: any = await import(pathToFileURL(join(app, 'node_modules', '@openai', 'agents-realtime', 'dist', 'index.mjs')).href);
    const cjs: any = createRequire(join(app, 'x.js'))('@openai/agents-realtime');
    expect(esm.build).toBe('esm');
    expect(cjs.build).toBe('cjs');
    expect(esm.RealtimeSession).not.toBe(cjs.RealtimeSession);
    for (const cls of [esm.RealtimeSession, cjs.RealtimeSession]) {
      expect(cls.prototype.connect.__weftPatched).toBe(true);
      expect(cls.prototype.close.__weftPatched).toBe(true);
    }
    expect(await weft.patchRealtimeSession()).toBe(true); // idempotent
  });

  it('still returns false when the package is nowhere, and keeps the explicit-module form', async () => {
    const empty = mkdtempSync(join(tmpdir(), 'weft-rt-none-'));
    vi.spyOn(process, 'cwd').mockReturnValue(empty);
    const argv = process.argv[1];
    process.argv[1] = join(empty, 'main.js');
    try {
      expect(await weft.patchRealtimeSession()).toBe(false);
    } finally {
      process.argv[1] = argv;
    }
    class RealtimeSession {
      async connect() {}
      close() {}
    }
    expect(await weft.patchRealtimeSession({ RealtimeSession })).toBe(true);
    expect((RealtimeSession.prototype.connect as any).__weftPatched).toBe(true);
    expect(await weft.patchRealtimeSession({})).toBe(false);
  });
});
