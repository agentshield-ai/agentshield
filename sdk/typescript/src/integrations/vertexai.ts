/**
 * Google Vertex AI (`@google-cloud/vertexai`).
 *
 * ```ts
 * const vertex = weft.wrapVertexAI(new VertexAI({ project, location }));
 * const model = vertex.getGenerativeModel({ model: 'gemini-2.0-flash' });   // traced
 * ```
 *
 * `generateContent`, `generateContentStream`, and chat sessions'
 * `sendMessage` / `sendMessageStream` become LLM calls with usage from
 * `usageMetadata` under the model name. Streams end with the SDK's aggregated
 * `response` promise. A model or chat session can also be wrapped directly.
 * (`@google/genai` clients in Vertex mode use `wrapGoogleGenAI`.)
 */
import type { Call } from '../call.js';
import { addGeminiUsage } from '../usage.js';
import { patchWith, plain, withCall } from './common.js';
import { integrationAttributes, integrationClient, integrationOp, pickOpSettings, type IntegrationOpSettings } from './settings.js';

const INTEGRATION = 'vertexai';

function modelName(m: any): string | undefined {
  const n = m?.model ?? m?.modelName ?? m?._model ?? m?.model_;
  return typeof n === 'string' ? n.split('/').pop() : undefined;
}

function recordVertexUsage(call: Call, model: string | undefined, resp: any): void {
  const u = resp?.usageMetadata;
  if (!u) return;
  addGeminiUsage(call, model, u);
}

function textOf(resp: any): string {
  return (resp?.candidates?.[0]?.content?.parts ?? []).filter((p: any) => p && !p.thought).map((p: any) => p.text ?? '').join('');
}

function traceMethod(owner: any, attr: string, op: string, model: () => string | undefined, stream: boolean, local: IntegrationOpSettings | undefined): void {
  patchWith(owner, attr, (orig) =>
    function (this: any, req: any, ...rest: any[]) {
      const client = integrationClient(INTEGRATION);
      if (!client) return orig.call(this, req, ...rest);
      const m = model();
      const meta = integrationOp(INTEGRATION, op, 'llm', {}, local);
      const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'chat', 'gen_ai.provider.name': 'gcp.vertex_ai' };
      if (m) attrs['gen_ai.request.model'] = m;
      const inputs: Record<string, unknown> = typeof req === 'string' || Array.isArray(req) ? { model: m, contents: req } : { model: m, ...(req ?? {}) };
      const call = client.createSpan(meta, inputs, { attributes: integrationAttributes(INTEGRATION, attrs, local) });
      const done = (resp: any, err?: unknown) => {
        try {
          if (resp) recordVertexUsage(call, m, resp);
        } catch {
          /* ignore */
        }
        client.finishSpan(call, resp ? { ...plain(resp), text: textOf(resp) } : undefined, err, meta);
      };
      let p: any;
      try {
        p = withCall(call, () => orig.call(this, req, ...rest));
      } catch (e) {
        done(undefined, e);
        throw e;
      }
      return Promise.resolve(p).then(
        (res: any) => {
          if (stream && res?.response && typeof res.response.then === 'function') {
            res.response.then(
              (r: any) => done(r),
              (e: unknown) => done(undefined, e),
            );
          } else done(res?.response ?? res);
          return res;
        },
        (e: unknown) => {
          done(undefined, e);
          throw e;
        },
      );
    },
  );
}

/** Trace a `GenerativeModel` (or preview model) instance. Its chat sessions are traced too. */
export function wrapVertexModel<T>(model: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  const local = pickOpSettings(opts);
  const m = model as any;
  const name = () => modelName(m);
  traceMethod(m, 'generateContent', 'vertexai.GenerativeModel.generate_content', name, false, local);
  traceMethod(m, 'generateContentStream', 'vertexai.GenerativeModel.generate_content_stream', name, true, local);
  patchWith(m, 'startChat', (orig) =>
    function (this: any, ...args: any[]) {
      const chat = orig.apply(this, args);
      return wrapVertexChat(chat, { ...opts, model: name() } as any);
    },
  );
  return model;
}

/** Trace a `ChatSession`: `sendMessage` and `sendMessageStream`. */
export function wrapVertexChat<T>(chat: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings; model?: string } = {}): T {
  const local = pickOpSettings(opts);
  const c = chat as any;
  const name = () => opts.model ?? modelName(c) ?? modelName(c?._model);
  traceMethod(c, 'sendMessage', 'vertexai.ChatSession.send_message', name, false, local);
  traceMethod(c, 'sendMessageStream', 'vertexai.ChatSession.send_message_stream', name, true, local);
  return chat;
}

/** Trace a `VertexAI` instance: models from `getGenerativeModel` (and `preview.getGenerativeModel`) are wrapped. */
export function wrapVertexAI<T>(vertex: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  const v = vertex as any;
  if (typeof v?.generateContent === 'function' && typeof v?.getGenerativeModel !== 'function') return wrapVertexModel(vertex, opts);
  for (const owner of [v, v?.preview]) {
    if (!owner) continue;
    patchWith(owner, 'getGenerativeModel', (orig) =>
      function (this: any, ...args: any[]) {
        return wrapVertexModel(orig.apply(this, args), opts);
      },
    );
  }
  return vertex;
}
