#!/usr/bin/env node
// Install (or uninstall) the Weft hooks into Claude Code's settings.json.
//
//   node install.mjs [--settings ~/.claude/settings.json] [--dry-run] [--uninstall]
//                    [--project team/proj] [--server-url URL] [--api-key-stdin | --api-key-from-env]
//
// The hooks run this directory's hooks/weft-hook.mjs by absolute path (quoted;
// a path with shell metacharacters is refused). Existing settings are kept: the
// first run saves a .bak copy (later runs never overwrite it), the new file is
// written atomically, and entries added earlier by this installer are replaced,
// so running it twice is safe. --project and --server-url are saved to
// ~/.weft-claude-code/settings.json (0600), and the API key too when given on
// stdin (--api-key-stdin) or taken from WEFT_API_KEY (--api-key-from-env); it is
// never accepted as a command-line argument, which other users and shell
// history can see. Environment variables WEFT_PROJECT, WEFT_SERVER_URL and
// WEFT_API_KEY win over the file.
// As an alternative, load the directory as a Claude Code plugin (see README).
import { chmodSync, readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { backupOnce, hookCommand, privateMkdir, readSecretFromStdin, writeFileAtomic, writePrivate } from './lib/weft.mjs';

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

/** The API key to save: from stdin or the environment, never from argv. Exits on `--api-key VALUE`. */
export async function apiKeyToSave({ argv = process.argv, env = process.env, stdin = process.stdin, prefix = 'weft' } = {}) {
  if (argv.includes('--api-key')) {
    console.error(`${prefix}: --api-key is not accepted (the key would be visible in the process list and shell history). Use --api-key-stdin (e.g. printf %s "$KEY" | node install.mjs --api-key-stdin) or --api-key-from-env with WEFT_API_KEY set.`);
    process.exit(2);
  }
  if (argv.includes('--api-key-stdin')) return (await readSecretFromStdin(stdin)) || undefined;
  if (argv.includes('--api-key-from-env')) return env.WEFT_API_KEY || undefined;
  return undefined;
}

async function main() {
  const here = dirname(fileURLToPath(import.meta.url));
  const settingsPath = resolve(arg('--settings') ?? join(homedir(), '.claude', 'settings.json'));
  const dry = process.argv.includes('--dry-run');
  const uninstall = process.argv.includes('--uninstall');
  const hook = join(here, 'hooks', 'weft-hook.mjs');
  let command;
  try {
    command = hookCommand(hook);
  } catch (e) {
    console.error(`weft-claude-code: ${e.message}`);
    process.exit(1);
  }
  const apiKey = uninstall ? undefined : await apiKeyToSave({ prefix: 'weft-claude-code' });
  let current = {};
  let raw = null;
  try {
    raw = readFileSync(settingsPath, 'utf8');
  } catch {
    /* no settings yet */
  }
  if (raw !== null) {
    try {
      current = JSON.parse(raw);
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
  const bak = backupOnce(settingsPath);
  if (bak) console.log(`weft-claude-code: saved a backup of the original settings to ${bak}`);
  writeFileAtomic(settingsPath, `${JSON.stringify(next, null, 2)}\n`);
  try {
    chmodSync(hook, 0o755);
  } catch {
    /* not critical */
  }
  const cfgDir = process.env.WEFT_CLAUDE_CODE_STATE_DIR ?? join(homedir(), '.weft-claude-code');
  const conf = { project: arg('--project'), server_url: arg('--server-url'), api_key: apiKey };
  if (!uninstall && Object.values(conf).some(Boolean)) {
    privateMkdir(cfgDir);
    const p = join(cfgDir, 'settings.json');
    let old = {};
    try {
      old = JSON.parse(readFileSync(p, 'utf8'));
    } catch {
      /* new file */
    }
    for (const [k, v] of Object.entries(conf)) if (v) old[k] = v;
    writePrivate(p, `${JSON.stringify(old, null, 2)}\n`);
    console.log(`weft-claude-code: saved settings to ${p}${apiKey ? ' (with the API key)' : ''}`);
  }
  console.log(`weft-claude-code: ${uninstall ? 'removed hooks from' : 'installed hooks into'} ${settingsPath}`);
  if (!uninstall && !process.env.WEFT_PROJECT && !conf.project) console.log('weft-claude-code: set WEFT_PROJECT (and WEFT_SERVER_URL, WEFT_API_KEY) or pass --project; tracing is off without a project.');
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
