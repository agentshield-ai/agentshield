export { init, finish, flush, WeftClient, DEFAULT_SERVER_URL, type InitOptions, type ClientSettings, type GetCallsOptions } from './client.js';
export { getClient, requireClient } from './global.js';
export { op, isOp, type Op, type OpOptions } from './op.js';
export { Call, CallFeedback, type CallData, type ApplyScorerResult } from './call.js';
export { attributes, withThread, getCurrentCall, withTracingDisabled } from './context.js';
export { Content, Image, Audio, Video, PDF, Html, Markdown } from './content.js';
export { Ref, isRefUri } from './refs.js';
export { WeftObject, Dataset, Model, Prompt, StringPrompt, MessagesPrompt, LLMStructuredCompletionModel, safeFormat, type PromptMessage } from './objects.js';
export {
  Evaluation,
  EvaluationLogger,
  ScoreLogger,
  autoSummarize,
  type EvaluationOptions,
  type EvaluationLoggerOptions,
  type EvalRowResult,
  type LogPredictionOptions,
} from './evaluation.js';
export {
  Scorer,
  ExactMatchScorer,
  ContainsScorer,
  RegexScorer,
  JSONValidScorer,
  LevenshteinScorer,
  LLMAsAJudgeScorer,
  HallucinationFreeScorer,
  SummarizationScorer,
  RelevanceScorer,
  ValidXMLScorer,
  JSONSchemaScorer,
  OpenAIModerationScorer,
  EmbeddingSimilarityScorer,
  RuleScorer,
  RemoteScorer,
  HALLUCINATION_PROMPT,
  SUMMARIZATION_PROMPT,
  RELEVANCE_PROMPT,
  evaluateExpr,
  schemaErrors,
  isWellFormedXml,
  levenshtein,
  type ScoreArgs,
} from './scorers.js';
export { AnnotationSpec, Leaderboard, Monitor, SavedView, type LeaderboardColumn } from './platform.js';
export { filterToClause, filtersToQuery, queryToFilters, toSeconds, OPERATOR_MAP, type ViewFilter, type SavedViewGetCallsOptions } from './savedView.js';
export {
  Conversation,
  Turn,
  LLM,
  Tool,
  SubAgent,
  SpanBase,
  Message,
  startConversation,
  startTurn,
  startLLM,
  startLLM as startLlm,
  startTool,
  startSubagent,
  endConversation,
  endTurn,
  endLLM,
  endLLM as endLlm,
  getCurrentConversation,
  getCurrentTurn,
  getCurrentLLM,
  getCurrentLLM as getCurrentLlm,
  logTurn,
  logConversation,
  withAgentNameOverride,
  type MessagePart,
  type Usage,
  type LogResult,
  type ConversationOptions,
  type TurnOptions,
  type LLMOptions,
  type ToolOptions,
  type SubAgentOptions,
  type LogTurnOptions,
  type LogConversationOptions,
} from './conversation.js';
export * as otel from './otel.js';
export { OpLoadError } from './loader.js';
export { Redactor, REDACTED, DEFAULT_REDACT_KEYS, redactPiiText, type RedactSettings } from './redact.js';
export { addUsage } from './usage.js';
export { WeftHTTPError, VERSION } from './http.js';
export { wrapOpenAI, wrapOpenAICompatible, type WrapOpenAIOptions } from './integrations/openai.js';
export { wrapGoogleGenAI } from './integrations/google_genai.js';
export { weftMiddleware } from './integrations/vercel_ai.js';
export { wrapAnthropic } from './integrations/anthropic.js';
export { canonicalJson, digestJson, digest, tableDigest } from './canonical.js';

import { requireClient } from './global.js';
import { Ref as RefClass, type Ref } from './refs.js';

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

/** Load what a ref (or "name", "name:v3") points to. */
export function get(s: string | Ref): Promise<any> {
  return requireClient().get(s);
}

/** Parse a `weft:///` (or `weave:///`) URI into a Ref. */
export function parseUri(uri: string): Ref {
  return RefClass.parse(uri);
}
