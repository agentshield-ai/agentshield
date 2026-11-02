#!/usr/bin/env node
// Copy the shared core into each Weft harness plugin (lib/weft.mjs).
// Run after editing plugins/weft-harness-common/weft.mjs:
//   node plugins/weft-harness-common/sync.mjs
import { copyFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
export const PLUGINS = ['weft-claude-code', 'weft-codex', 'weft-openclaw'];
for (const p of PLUGINS) {
  const dst = join(here, '..', p, 'lib', 'weft.mjs');
  mkdirSync(dirname(dst), { recursive: true });
  copyFileSync(join(here, 'weft.mjs'), dst);
  console.log(`synced ${dst}`);
}
