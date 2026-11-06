#!/usr/bin/env node
// Install (or uninstall) the Weft Stop hook for the OpenAI Codex CLI.
//
//   node install.mjs [--hooks ~/.codex/hooks.json] [--dry-run] [--uninstall] [--print-toml]
//                    [--project team/proj] [--server-url URL] [--api-key-stdin | --api-key-from-env]
//
// Merges a Stop hook running hooks/weft-codex-hook.mjs into $CODEX_HOME/hooks.json
// (the first run keeps a .bak copy that later runs never overwrite; the file is
// written atomically; earlier entries of this installer are replaced). The hook
// path is quoted, and refused when it holds shell metacharacters. The API key is
// read from stdin (--api-key-stdin) or WEFT_API_KEY (--api-key-from-env), never
// from argv. --print-toml prints the equivalent config.toml blocks instead: a
// [[hooks.Stop]] table, and the `notify` fallback for setups that only allow
// managed hooks.
import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { backupOnce, hookCommand, privateMkdir, readSecretFromStdin, writeFileAtomic, writePrivate } from './lib/weft.mjs';

const MARK = 'weft-codex-hook.mjs';

export function mergeStopHook(doc, command, { uninstall = false } = {}) {
  const out = { ...doc, hooks: { ...(doc.hooks ?? {}) } };
  const groups = (out.hooks.Stop ?? []).map((g) => ({ ...g, hooks: (g.hooks ?? []).filter((h) => !String(h.command ?? '').includes(MARK)) })).filter((g) => g.hooks.length);
  if (!uninstall) groups.push({ hooks: [{ type: 'command', command, timeout: 5 }] });
  if (groups.length) out.hooks.Stop = groups;
  else delete out.hooks.Stop;
  if (!Object.keys(out.hooks).length) delete out.hooks;
  return out;
}

/** A TOML basic string (JSON string escaping is valid TOML for these). */
const tomlString = (s) => JSON.stringify(String(s));

export function tomlSnippet(hook, platform = process.platform) {
  const command = hookCommand(hook, platform); // throws for unsafe paths
  return `# --- Weft tracing (Stop hook) ----------------------------------------------
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = ${tomlString(command)}
timeout = 5

# --- or, where only managed hooks are allowed: Codex's notify program -------
# notify = [${tomlString('node')}, ${tomlString(hook)}]
`;
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
  const hook = join(here, 'hooks', 'weft-codex-hook.mjs');
  let command;
  try {
    if (process.argv.includes('--print-toml')) {
      console.log(tomlSnippet(hook));
      return;
    }
    command = hookCommand(hook);
  } catch (e) {
    console.error(`weft-codex: ${e.message}`);
    process.exit(1);
  }
  const codexHome = process.env.CODEX_HOME || join(homedir(), '.codex');
  const path = resolve(arg('--hooks') ?? join(codexHome, 'hooks.json'));
  const uninstall = process.argv.includes('--uninstall');
  const apiKey = uninstall ? undefined : await apiKeyToSave({ prefix: 'weft-codex' });
  let doc = {};
  let raw = null;
  try {
    raw = readFileSync(path, 'utf8');
  } catch {
    /* no hooks file yet */
  }
  if (raw !== null) {
    try {
      doc = JSON.parse(raw);
    } catch (e) {
      console.error(`weft-codex: ${path} is not valid JSON (${e.message}); not changing it.`);
      process.exit(1);
    }
  }
  const next = mergeStopHook(doc, command, { uninstall });
  if (process.argv.includes('--dry-run')) {
    console.log(JSON.stringify(next, null, 2));
    return;
  }
  const bak = backupOnce(path);
  if (bak) console.log(`weft-codex: saved a backup of the original hooks to ${bak}`);
  writeFileAtomic(path, `${JSON.stringify(next, null, 2)}\n`);
  const conf = { project: arg('--project'), server_url: arg('--server-url'), api_key: apiKey };
  if (!uninstall && Object.values(conf).some(Boolean)) {
    const dir = process.env.WEFT_CODEX_STATE_DIR ?? join(homedir(), '.weft-codex');
    privateMkdir(dir);
    const p = join(dir, 'settings.json');
    let old = {};
    try {
      old = JSON.parse(readFileSync(p, 'utf8'));
    } catch {
      /* new */
    }
    for (const [k, v] of Object.entries(conf)) if (v) old[k] = v;
    writePrivate(p, `${JSON.stringify(old, null, 2)}\n`);
    console.log(`weft-codex: saved settings to ${p}${apiKey ? ' (with the API key)' : ''}`);
  }
  console.log(`weft-codex: ${uninstall ? 'removed the Stop hook from' : 'added a Stop hook to'} ${path}`);
  if (!uninstall) console.log('weft-codex: Codex asks to trust new hooks on the next launch; approve it (or set bypass_hook_trust = true).');
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
