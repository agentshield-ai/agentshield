// Weft tracing extension for the Pi coding agent: records each prompt as an
// `invoke_agent` turn with its LLM calls and tool executions, in a Weft agent
// conversation per Pi session. See README.md.
//
// As a Pi package (`pi install ./plugins/weft-pi`) this file is the extension
// (default export). With the SDK, pass `createWeftExtension({ project })` to
// `DefaultResourceLoader({ extensionFactories: [...] })`.
import { registerWeft } from './lib/pi.mjs';

/** An extension factory with explicit options (`project`, `serverUrl`, `apiKey`, `agentName`, `captureContent`, ...). */
export function createWeftExtension(options = {}) {
  return function weftExtension(pi) {
    registerWeft(pi, options);
  };
}

/** The default extension: configured from WEFT_* variables or ~/.weft-pi/settings.json. */
export default function weft(pi) {
  registerWeft(pi);
}

export { registerWeft, WeftPiTracer, resolveConfig, chatMessage, EVENTS, PLUGIN, PLUGIN_VERSION } from './lib/pi.mjs';
