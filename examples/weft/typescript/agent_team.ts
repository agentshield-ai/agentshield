/**
 * A two-agent team: an orchestrator delegates research to a researcher agent,
 * then hands off to a writer; both record their reasoning.
 *
 *   cd sdk/typescript && npm install && npm run build
 *   WEFT_SERVER_URL=http://localhost:6345 node examples/weft/typescript/agent_team.ts
 *
 * Then open the project in the UI: Sessions -> the "trip-oslo-…" session shows
 * the timeline (reasoning tied to the tool call it led to), the Graph tab shows
 * orchestrator -> researcher (delegate, and the return) and
 * orchestrator -> writer (hand-off); Teams -> "Trip planners" aggregates them.
 */
import { randomUUID } from 'node:crypto';
import * as weft from '../../../sdk/typescript/dist/index.js';

const client = await weft.init('ts-agent-team-demo', { settings: { printCallLink: false } });
const session = `trip-oslo-${randomUUID().slice(0, 6)}`;

const webSearch = weft.trace(
  function webSearch(_query: string) {
    return ['Capital of Norway', 'On the Oslofjord', 'Home of the Nobel Peace Prize'];
  },
  { kind: 'tool' },
);

const conv = weft.startConversation({ agentName: 'orchestrator', conversationId: session, team: { id: 'trip-planners', name: 'Trip planners' } });
await conv.run(async (c) => {
  await c.startTurn({ userMessage: 'Find three facts about Oslo and summarise them' }).run(async (turn) => {
    // The orchestrator's model call: its thinking is a reasoning step, linked by
    // the server to the tool call it asked for (toolu_1).
    await turn.startLLM({ model: 'claude-sonnet-4', providerName: 'anthropic' }).run((llm) => {
      llm.inputMessages = [weft.Message.user('Find three facts about Oslo and summarise them')];
      llm.think('The user wants facts; the researcher agent should gather them.');
      llm.outputMessages = [weft.Message.assistant('', { toolCalls: [{ id: 'toolu_1', name: 'delegate_research', arguments: { task: 'Find three facts about Oslo' } }] })];
      llm.usage = { inputTokens: 200, outputTokens: 120, reasoningTokens: 40 };
    });

    await turn.startTool({ name: 'delegate_research', arguments: { task: 'Find three facts about Oslo' }, toolCallId: 'toolu_1' }).run(async (tool) => {
      // A delegation: the researcher's turn runs inside the hand-off, so it nests
      // in the same trace and the server records the delegate edge (and the return).
      await weft.startHandoff('researcher', { input: 'Find three facts about Oslo', kind: 'delegate' }).run(async (h) => {
        await c.startTurn({ agentName: 'researcher', userMessage: 'Find three facts about Oslo' }).run(async (rt) => {
          weft.reasoning('Search the web first, then pick three facts.');
          const facts = await webSearch('Oslo facts');
          rt.outputMessages = [weft.Message.assistant(facts.join('; '))];
          h.output = facts;
        });
      });
      tool.result = 'three facts';
    });

    // A summary step, then an instant hand-off to the writer.
    weft.reasoning('Facts are in; the writer agent should summarise.', { kind: 'summary' });
    turn.handoff('writer', { input: 'Summarise the three facts' });
    await turn.startSubagent({ name: 'writer', model: 'gpt-4o' }).run(async (w) => {
      await w.startLLM({ model: 'gpt-4o', providerName: 'openai' }).run((l) => {
        l.output("Oslo, Norway's capital on the Oslofjord, is home of the Nobel Peace Prize.");
      });
    });
    turn.outputMessages = [weft.Message.assistant("Oslo, Norway's capital on the Oslofjord, is home of the Nobel Peace Prize.")];
  });
});

await weft.finish();
console.log(`session ${session} in project ${client.projectId}`);
