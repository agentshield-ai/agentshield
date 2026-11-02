#!/usr/bin/env node
// Install (or uninstall) the Weft hooks into Claude Code's settings.json.
//
//   node install.mjs [--settings ~/.claude/settings.json] [--dry-run] [--uninstall]
//                    [--project team/proj] [--server-url URL] [--api-key KEY]
//
// The hooks run this directory's hooks/weft-hook.mjs by absolute path. Existing
// settings are kept (a .bak copy is written first); entries added earlier by
// this installer are replaced, so running it twice is safe. --project,
// --server-url and --api-key are saved to ~/.weft-claude-code/settings.json
// (environment variables WEFT_PROJECT, WEFT_SERVER_URL, WEFT_API_KEY win).
// As an alternative, load the directory as a Claude Code plugin (see README).
import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync, chmodSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const EVENTS = ['SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PostToolUseFailure', 'Stop', 'SubagentStop', 'SessionEnd'];
const MATCHED = new Set(['PreToolUse', 'PostToolUse', 'PostToolUseFailure']);
const MARK = 'weft-hook.mjs';

export function mergeHooks(settings, command, { uninstall = false } = {}) {
  const out = { ...settings, hooks: { ...(settings.hooks ?? {}) } };
  for (const ev of EVENTS) {
    const groups = (out.hooks[ev] ?? [])
      .map((g) => ({ ...g, hooks: (g.hooks ?? []).filter((h) => !String(h.command ?? '').includes(MARK)) }))
      .filter((g) => g.hooks.length);
    if (!uninstall) groups.push({ ...(MATCHED.has(ev) ? { matcher: '*' } : {}), hooks: [{ type: 'command', command, timeout: 5 }] });
    if (groups.length) out.hooks[ev] = groups;
    else delete out.hooks[ev];
  }
  if (!Object.keys(out.hooks).length) delete out.hooks;
  return out;
}

function arg(name) {
  const i = process.argv.indexOf(name);
  return i > 0 ? process.argv[i + 1] : undefined;
}

function main() {
  const here = dirname(fileURLToPath(import.meta.url));
  const settingsPath = resolve(arg('--settings') ?? join(homedir(), '.claude', 'settings.json'));
  const dry = process.argv.includes('--dry-run');
  const uninstall = process.argv.includes('--uninstall');
  const hook = join(here, 'hooks', 'weft-hook.mjs');
  const command = `node "${hook}"`;
  let current = {};
  if (existsSync(settingsPath)) {
    try {
      current = JSON.parse(readFileSync(settingsPath, 'utf8'));
    } catch (e) {
      console.error(`weft-claude-code: ${settingsPath} is not valid JSON (${e.message}); not changing it.`);
      process.exit(1);
    }
  }
  const next = mergeHooks(current, command, { uninstall });
  if (dry) {
    console.log(JSON.stringify(next, null, 2));
    return;
  }
  mkdirSync(dirname(settingsPath), { recursive: true });
  if (existsSync(settingsPath)) copyFileSync(settingsPath, `${settingsPath}.bak`);
  writeFileSync(settingsPath, `${JSON.stringify(next, null, 2)}\n`);
  try {
    chmodSync(hook, 0o755);
  } catch {
    /* not critical */
  }
  const cfgDir = process.env.WEFT_CLAUDE_CODE_STATE_DIR ?? join(homedir(), '.weft-claude-code');
  const conf = { project: arg('--project'), server_url: arg('--server-url'), api_key: arg('--api-key') };
  if (!uninstall && Object.values(conf).some(Boolean)) {
    mkdirSync(cfgDir, { recursive: true, mode: 0o700 });
    const p = join(cfgDir, 'settings.json');
    let old = {};
    try {
      old = JSON.parse(readFileSync(p, 'utf8'));
    } catch {
      /* new file */
    }
    for (const [k, v] of Object.entries(conf)) if (v) old[k] = v;
    writeFileSync(p, `${JSON.stringify(old, null, 2)}\n`, { mode: 0o600 });
    console.log(`weft-claude-code: saved settings to ${p}`);
  }
  console.log(`weft-claude-code: ${uninstall ? 'removed hooks from' : 'installed hooks into'} ${settingsPath}`);
  if (!uninstall && !process.env.WEFT_PROJECT && !conf.project) console.log('weft-claude-code: set WEFT_PROJECT (and WEFT_SERVER_URL, WEFT_API_KEY) or pass --project; tracing is off without a project.');
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
