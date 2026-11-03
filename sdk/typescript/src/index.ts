export {
  init,
  finish,
  flush,
  WeftClient,
  DEFAULT_SERVER_URL,
  agentConversationPath,
  type CustomRuntimeId,
  type CustomRuntimeApplyRes,
  type RegisterCustomRuntimeOptions,
  type RegisterCustomRuntimeResult,
  senderOptionsFromEnv,
  type InitOptions,
  type ClientSettings,
  type GetCallsOptions,
  type RegistryCollection,
  type RegistryVersion,
} from './client.js';
export { logger, setLogLevel, getLogLevel, parseLogLevel, type LogLevel } from './log.js';
export { getClient, requireClient } from './global.js';
export { op, isOp, type Op, type OpOptions } from './op.js';
export { Call, CallFeedback, type CallData, type ApplyScorerResult } from './call.js';
export { attributes, withThread, getCurrentCall, withTracingDisabled, withTeam, getCurrentTeam, type TeamInfo } from './context.js';
export { scrubReasoning, hasReasoning, reasoningStep, normReasoningKind, reasoningDigest, type ReasoningKind, type ReasoningStep } from './reasoning.js';
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
  Handoff,
  handoff,
  startHandoff,
  reasoning,
  currentAgentName,
  type HandoffOptions,
  type TeamLike,
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
export {
  Redactor,
  REDACTED,
  DEFAULT_REDACT_KEYS,
  DEFAULT_PII_ENTITIES,
  redactPiiText,
  presidioAnalyzer,
  applyPiiSpans,
  type RedactSettings,
  type PiiAnalyzer,
  type PiiSpan,
} from './redact.js';
export { addUsage } from './usage.js';
export { WeftHTTPError, VERSION } from './http.js';
export { wrapOpenAI, wrapOpenAICompatible, wrapGroq, wrapTogether, wrapCerebras, type WrapOpenAIOptions } from './integrations/openai.js';
export { wrapGoogleGenAI } from './integrations/google_genai.js';
export { weftMiddleware, wrapAITools, wrapAISDK, type WeftMiddlewareOptions } from './integrations/vercel_ai.js';
export { wrapAnthropic } from './integrations/anthropic.js';
export { WeftCallbackHandler, weftCallbackHandler } from './integrations/langchain.js';
export { instrumentLlamaIndex, type LlamaIndexInstrumentation } from './integrations/llamaindex.js';
export { wrapMCPClient, wrapMCPServer, instrumentMCP } from './integrations/mcp.js';
export { wrapClaudeAgentSDK, wrapClaudeAgentQuery } from './integrations/claude_agent_sdk.js';
export { WeftTracingProcessor, instrumentOpenAIAgents } from './integrations/openai_agents.js';
export { wrapMistral, MistralStreamAccumulator } from './integrations/mistral.js';
export { wrapCohere, CohereStreamAccumulator } from './integrations/cohere.js';
export { wrapBedrock, instrumentBedrock, wrapBedrockAgent, instrumentBedrockAgent } from './integrations/bedrock.js';
export { AgentTraceProcessor as BedrockAgentTraceProcessor } from './integrations/bedrock_agents.js';
export {
  wrapOpenAIRealtime,
  instrumentOpenAIRealtime,
  RealtimeTracer,
  wrapRealtimeSession,
  instrumentRealtimeSession,
  patchRealtimeSession,
  pcmToWav,
  audioFormat,
  type AudioFormat,
} from './integrations/openai_realtime.js';
export { wrapVertexAI, wrapVertexModel, wrapVertexChat } from './integrations/vertexai.js';
export { wrapTypeSafe, patchTypeSafeModule, instrumentTypeSafe } from './integrations/typesafe.js';
export {
  configureIntegrations,
  updateIntegration,
  getIntegrationSettings,
  integrationEnabled,
  canonicalIntegration,
  type AutopatchSettings,
  type IntegrationSettings,
  type IntegrationOpSettings,
} from './integrations/settings.js';
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

/**
 * Link any published object version into a registry collection
 * (`targetPath`: `<registry_project>/<collection>` or `<entity>/<registry_project>/<collection>`).
 */
export function linkToRegistry(obj: unknown, opts: { targetPath: string; aliases?: string[] }): Promise<{ versionIndex: number; ref: string; targetPath: string }> {
  return requireClient().linkToRegistry(obj, opts);
}

/** Weave's `link_prompt_to_registry`: link a published prompt version into a registry collection. */
export function linkPromptToRegistry(prompt: unknown, opts: { targetPath: string; aliases?: string[] }): Promise<{ versionIndex: number; ref: string; targetPath: string }> {
  return requireClient().linkPromptToRegistry(prompt, opts);
}

/** Link calls of the global client to a W&B run (`wb_run_id`, `wb_run_step`); `null` clears it. */
export function setWbRun(runId: string | null, step?: number | (() => number | undefined)): void {
  requireClient().setWbRun(runId, step);
}

/** Parse a `weft:///` (or `weave:///`) URI into a Ref. */
export function parseUri(uri: string): Ref {
  return RefClass.parse(uri);
}
