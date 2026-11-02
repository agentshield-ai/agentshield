#!/usr/bin/env node
// Install (or uninstall) the Weft Stop hook for the OpenAI Codex CLI.
//
//   node install.mjs [--hooks ~/.codex/hooks.json] [--dry-run] [--uninstall] [--print-toml]
//                    [--project team/proj] [--server-url URL] [--api-key KEY]
//
// Merges a Stop hook running hooks/weft-codex-hook.mjs into $CODEX_HOME/hooks.json
// (a .bak copy is kept; earlier entries of this installer are replaced).
// --print-toml prints the equivalent config.toml blocks instead: a [[hooks.Stop]]
// table, and the `notify` fallback for setups that only allow managed hooks.
import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

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

export function tomlSnippet(hook) {
  return `# --- Weft tracing (Stop hook) ----------------------------------------------
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = 'node "${hook}"'
timeout = 5

# --- or, where only managed hooks are allowed: Codex's notify program -------
# notify = ["node", "${hook}"]
`;
}

function arg(name) {
  const i = process.argv.indexOf(name);
  return i > 0 ? process.argv[i + 1] : undefined;
}

function main() {
  const here = dirname(fileURLToPath(import.meta.url));
  const hook = join(here, 'hooks', 'weft-codex-hook.mjs');
  if (process.argv.includes('--print-toml')) {
    console.log(tomlSnippet(hook));
    return;
  }
  const codexHome = process.env.CODEX_HOME || join(homedir(), '.codex');
  const path = resolve(arg('--hooks') ?? join(codexHome, 'hooks.json'));
  const uninstall = process.argv.includes('--uninstall');
  let doc = {};
  if (existsSync(path)) {
    try {
      doc = JSON.parse(readFileSync(path, 'utf8'));
    } catch (e) {
      console.error(`weft-codex: ${path} is not valid JSON (${e.message}); not changing it.`);
      process.exit(1);
    }
  }
  const next = mergeStopHook(doc, `node "${hook}"`, { uninstall });
  if (process.argv.includes('--dry-run')) {
    console.log(JSON.stringify(next, null, 2));
    return;
  }
  mkdirSync(dirname(path), { recursive: true });
  if (existsSync(path)) copyFileSync(path, `${path}.bak`);
  writeFileSync(path, `${JSON.stringify(next, null, 2)}\n`);
  const conf = { project: arg('--project'), server_url: arg('--server-url'), api_key: arg('--api-key') };
  if (!uninstall && Object.values(conf).some(Boolean)) {
    const dir = process.env.WEFT_CODEX_STATE_DIR ?? join(homedir(), '.weft-codex');
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    const p = join(dir, 'settings.json');
    let old = {};
    try {
      old = JSON.parse(readFileSync(p, 'utf8'));
    } catch {
      /* new */
    }
    for (const [k, v] of Object.entries(conf)) if (v) old[k] = v;
    writeFileSync(p, `${JSON.stringify(old, null, 2)}\n`, { mode: 0o600 });
    console.log(`weft-codex: saved settings to ${p}`);
  }
  console.log(`weft-codex: ${uninstall ? 'removed the Stop hook from' : 'added a Stop hook to'} ${path}`);
  if (!uninstall) console.log('weft-codex: Codex asks to trust new hooks on the next launch; approve it (or set bypass_hook_trust = true).');
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
