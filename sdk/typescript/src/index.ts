export { init, finish, flush, WeftClient, DEFAULT_SERVER_URL, type InitOptions, type GetCallsOptions } from './client.js';
export { getClient, requireClient } from './global.js';
export { op, isOp, type Op, type OpOptions } from './op.js';
export { Call, CallFeedback, type CallData } from './call.js';
export { attributes, withThread, getCurrentCall } from './context.js';
export { Ref } from './refs.js';
export { WeftObject, Dataset } from './objects.js';
export { Evaluation, autoSummarize, type EvaluationOptions, type EvalRowResult } from './evaluation.js';
export { addUsage } from './usage.js';
export { WeftHTTPError, VERSION } from './http.js';
export { wrapOpenAI, type WrapOpenAIOptions } from './integrations/openai.js';
export { wrapAnthropic } from './integrations/anthropic.js';
export { canonicalJson, digest } from './canonical.js';

import { requireClient } from './global.js';
import type { Ref } from './refs.js';

/** Publish an object (Dataset, Evaluation, op, or any JSON value) and return its ref. */
export function publish(obj: unknown, name?: string): Promise<Ref> {
  return requireClient().publish(obj, name);
}

/** A ref from "name", "name:v3", "name:latest", a digest or a full URI; load it with `.get()`. */
export function ref(s: string): Ref & { get: () => Promise<any> } {
  const client = requireClient();
  const r = client.ref(s) as Ref & { get: () => Promise<any> };
  Object.defineProperty(r, 'get', { value: () => client.get(r), enumerable: false });
  return r;
}
