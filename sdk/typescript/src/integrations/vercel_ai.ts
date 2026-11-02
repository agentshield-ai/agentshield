import { currentContext, storage } from '../context.js';
import { syntheticOp } from '../client.js';
import { tracingClient } from '../global.js';
import { addUsage } from '../usage.js';

function usageOf(u: any): { p: number; c: number; t?: number } | null {
  if (!u) return null;
  const p = u.inputTokens ?? u.promptTokens;
  const c = u.outputTokens ?? u.completionTokens;
  if (p === undefined && c === undefined) return null;
  const num = (x: any) => (typeof x === 'number' ? x : typeof x?.total === 'number' ? x.total : 0);
  return { p: num(p), c: num(c), t: typeof u.totalTokens === 'number' ? u.totalTokens : undefined };
}

function inputsOf(params: any, model: any): Record<string, unknown> {
  const { abortSignal: _a, headers: _h, ...rest } = params ?? {};
  return { model: model?.modelId, provider: model?.provider, ...rest };
}

/**
 * Vercel AI SDK middleware: `wrapLanguageModel({ model, middleware: weftMiddleware() })`.
 * Each generate/stream request becomes an `llm` call with the model's usage.
 */
export function weftMiddleware(opts: { name?: string } = {}): Record<string, any> {
  const prefix = opts.name ?? 'vercel_ai';
  return {
    async wrapGenerate({ doGenerate, params, model }: any) {
      const client = tracingClient();
      if (!client) return doGenerate();
      const call = client.createCall(syntheticOp(`${prefix}.generate`, 'llm'), inputsOf(params, model));
      try {
        const res = await storage.run({ ...currentContext(), stack: [...currentContext().stack, call] }, () => doGenerate());
        const u = usageOf(res?.usage);
        if (u) addUsage(call, model?.modelId, u.p, u.c, u.t);
        const { request: _r, response: _resp, rawCall: _rc, ...out } = res ?? {};
        client.finishCall(call, out);
        return res;
      } catch (e) {
        client.finishCall(call, undefined, e);
        throw e;
      }
    },
    async wrapStream({ doStream, params, model }: any) {
      const client = tracingClient();
      if (!client) return doStream();
      const call = client.createCall(syntheticOp(`${prefix}.stream`, 'llm'), inputsOf(params, model));
      let res: any;
      try {
        res = await storage.run({ ...currentContext(), stack: [...currentContext().stack, call] }, () => doStream());
      } catch (e) {
        client.finishCall(call, undefined, e);
        throw e;
      }
      let text = '';
      let finishReason: unknown = null;
      const toolCalls: unknown[] = [];
      let done = false;
      const finish = (err?: unknown) => {
        if (done) return;
        done = true;
        client.finishCall(call, { text, finishReason, toolCalls }, err);
      };
      const transform = new TransformStream({
        transform(chunk: any, controller) {
          if (chunk?.type === 'text-delta' || chunk?.type === 'text') text += chunk.delta ?? chunk.textDelta ?? chunk.text ?? '';
          if (chunk?.type === 'tool-call') toolCalls.push(chunk);
          if (chunk?.type === 'finish') {
            finishReason = chunk.finishReason;
            const u = usageOf(chunk.usage);
            if (u) addUsage(call, model?.modelId, u.p, u.c, u.t);
          }
          if (chunk?.type === 'error') finish(chunk.error);
          controller.enqueue(chunk);
        },
        flush() {
          finish();
        },
      });
      return { ...res, stream: res.stream.pipeThrough(transform) };
    },
  };
}
