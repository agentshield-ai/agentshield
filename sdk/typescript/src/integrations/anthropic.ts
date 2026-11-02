import type { Call } from '../call.js';
import { syntheticOp } from '../client.js';
import { currentContext } from '../context.js';
import { tracingClient } from '../global.js';
import { toJson } from '../serialize.js';
import { addUsage } from '../usage.js';
import { patchMethod, type Accumulator } from './common.js';

export class MessageAccumulator implements Accumulator {
  private msg: any = {};
  private content: any[] = [];

  add(e: any): void {
    if (!e || typeof e !== 'object') return;
    switch (e.type) {
      case 'message_start':
        this.msg = { ...(e.message ?? {}), usage: { ...(e.message?.usage ?? {}) } };
        break;
      case 'content_block_start':
        this.content[e.index ?? this.content.length] = { ...(e.content_block ?? {}) };
        break;
      case 'content_block_delta': {
        const b = (this.content[e.index ?? 0] ??= { type: 'text', text: '' });
        const d = e.delta ?? {};
        if (d.type === 'text_delta') b.text = (b.text ?? '') + (d.text ?? '');
        else if (d.type === 'input_json_delta') b._partial = (b._partial ?? '') + (d.partial_json ?? '');
        else if (d.type === 'thinking_delta') b.thinking = (b.thinking ?? '') + (d.thinking ?? '');
        else if (d.type === 'signature_delta') b.signature = d.signature;
        break;
      }
      case 'content_block_stop': {
        const b = this.content[e.index ?? 0];
        if (b && b._partial !== undefined) {
          try {
            b.input = b._partial ? JSON.parse(b._partial) : {};
          } catch {
            b.input = b._partial;
          }
          delete b._partial;
        }
        break;
      }
      case 'message_delta':
        for (const [k, v] of Object.entries(e.delta ?? {})) if (v != null) this.msg[k] = v;
        if (e.usage) for (const [k, v] of Object.entries(e.usage)) if (v != null) (this.msg.usage ??= {})[k] = v;
        break;
    }
  }

  result(): any {
    for (const b of this.content) if (b) delete b._partial;
    return { ...this.msg, content: this.content.filter(Boolean) };
  }
}

export function recordAnthropicUsage(call: Call, out: any): void {
  const u = out?.usage;
  if (!u) return;
  addUsage(call, out.model, u.input_tokens ?? 0, u.output_tokens ?? 0);
}

/**
 * Trace an Anthropic client instance: `messages.create` (incl. `stream: true`)
 * and `messages.stream()`. Returns the same client.
 */
export function wrapAnthropic<T>(client: T): T {
  const c = client as any;
  const messages = c?.messages;
  patchMethod(messages, 'create', {
    opName: 'anthropic.messages.create',
    accumulator: () => new MessageAccumulator(),
    recordUsage: recordAnthropicUsage,
  });
  const origStream = messages?.stream;
  if (typeof origStream === 'function' && !origStream.__weftPatched) {
    const bound = origStream.bind(messages);
    const wrapped = (body: any, ...rest: any[]) => {
      const wc = tracingClient();
      const s = bound(body, ...rest);
      if (!wc || !s || typeof s.finalMessage !== 'function') return s;
      const call = wc.createCall(syntheticOp('anthropic.messages.stream', 'llm'), toJson(body ?? {}) as Record<string, unknown>, { parent: currentContext().stack.at(-1) ?? null });
      s.finalMessage().then(
        (m: any) => {
          const out = toJson(m);
          recordAnthropicUsage(call, out);
          wc.finishCall(call, out);
        },
        (e: unknown) => wc.finishCall(call, undefined, e),
      );
      return s;
    };
    Object.defineProperty(wrapped, '__weftPatched', { value: true });
    messages.stream = wrapped;
  }
  return client;
}
