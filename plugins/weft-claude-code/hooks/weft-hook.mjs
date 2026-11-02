#!/usr/bin/env node
// Claude Code hook entry point for Weft tracing.
//
//   hook mode:  node weft-hook.mjs            (payload JSON on stdin; registered for every hook event)
//   worker:     node weft-hook.mjs --flush <session_id>
//
// The hook never blocks or fails the agent: it reads stdin with a time limit,
// appends a record to a local session log, maybe starts the detached flush
// worker, prints nothing and exits 0.
import { config, flushSession, handleHook } from '../lib/claude.mjs';

async function readStdin(limitMs = 1000, maxBytes = 8 << 20) {
  if (process.stdin.isTTY) return '';
  return new Promise((resolve) => {
    const chunks = [];
    let size = 0;
    const done = () => resolve(Buffer.concat(chunks).toString('utf8'));
    const timer = setTimeout(done, limitMs);
    timer.unref();
    process.stdin.on('data', (c) => {
      size += c.length;
      if (size <= maxBytes) chunks.push(c);
    });
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

async function main() {
  const args = process.argv.slice(2);
  if (args[0] === '--flush') {
    const cfg = config();
    if (!cfg.disabled && args[1]) await flushSession(cfg, args[1], { transcriptDelayMs: Number(process.env.WEFT_TRANSCRIPT_DELAY_MS ?? 300) });
    return;
  }
  const raw = await readStdin();
  let payload = null;
  try {
    payload = raw.trim() ? JSON.parse(raw) : null;
  } catch {
    payload = null;
  }
  if (payload && args[0] && !payload.hook_event_name) payload.hook_event_name = args[0];
  const mode = process.env.WEFT_FLUSH_MODE === 'inline' ? 'inline' : process.env.WEFT_FLUSH_MODE === 'off' ? 'off' : 'background';
  const r = handleHook(payload, { flush: mode });
  if (r?.flush) await r.flush; // inline mode (tests, CI): bounded by the network timeout
}

main()
  .catch(() => {})
  .finally(() => process.exit(0));
