#!/usr/bin/env node
// Codex entry point for Weft tracing.
//
//   Stop hook:  node weft-codex-hook.mjs                       (hook JSON on stdin)
//   notify:     node weft-codex-hook.mjs '<agent-turn-complete JSON>'   (notify = [...] in config.toml)
//   worker:     node weft-codex-hook.mjs --flush <rollout.jsonl> [session_id]
//
// The hook and notify modes only start the detached collector and exit 0
// without output, so Codex never waits on the network.
import { config, flushRollout, handleTrigger } from '../lib/codex.mjs';

async function readStdin(limitMs = 1000) {
  if (process.stdin.isTTY) return '';
  return new Promise((resolve) => {
    const chunks = [];
    const done = () => resolve(Buffer.concat(chunks).toString('utf8'));
    const timer = setTimeout(done, limitMs);
    timer.unref();
    process.stdin.on('data', (c) => chunks.push(c));
    process.stdin.on('end', () => {
      clearTimeout(timer);
      done();
    });
    process.stdin.on('error', () => {
      clearTimeout(timer);
      done();
    });
  });
}

function parse(s) {
  try {
    return s && s.trim() ? JSON.parse(s) : null;
  } catch {
    return null;
  }
}

async function main() {
  const args = process.argv.slice(2);
  if (args[0] === '--flush') {
    const cfg = config();
    if (!cfg.disabled && args[1]) {
      await new Promise((r) => setTimeout(r, Number(process.env.WEFT_ROLLOUT_DELAY_MS ?? 500))); // let Codex finish writing the rollout
      await flushRollout(cfg, args[1], { sessionId: args[2] || undefined });
    }
    return;
  }
  // notify passes the payload as the last argument; hooks pass it on stdin.
  const payload = args.length ? parse(args[args.length - 1]) : parse(await readStdin());
  const mode = process.env.WEFT_FLUSH_MODE === 'inline' ? 'inline' : process.env.WEFT_FLUSH_MODE === 'off' ? 'off' : 'background';
  const r = handleTrigger(payload, { mode });
  if (r?.result) await r.result;
}

main()
  .catch(() => {})
  .finally(() => process.exit(0));
