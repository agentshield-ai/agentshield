// Weft tracing plugin for OpenClaw: records agent runs, model calls, tools and
// sub-agents as Weft agent conversations. See README.md.
import { registerWeft } from './lib/openclaw.mjs';

const plugin = {
  id: 'weft',
  name: 'Weft',
  description: 'Trace OpenClaw agent runs into Weft as agent conversations',
  register(api) {
    registerWeft(api);
  },
};

export default plugin;
