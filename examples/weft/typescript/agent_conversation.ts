/**
 * Agent conversation tracing (Weave's start_conversation API in TypeScript), an
 * agent eval linking existing traces, and a saved view.
 *
 *   cd sdk/typescript && npm install && npm run build
 *   WEFT_SERVER_URL=http://localhost:6345 node examples/weft/typescript/agent_conversation.ts
 */
import * as weft from '../../../sdk/typescript/dist/index.js';

const client = await weft.init('ts-agent-conversation-demo');

const lookupOrder = weft.op(
  function lookupOrder(orderId: number) {
    return { orderId, status: 'shipped', eta: 'Friday' };
  },
  { kind: 'tool' },
);

const turns: weft.Turn[] = [];
const conv = weft.startConversation({ agentName: 'support-bot', conversationName: 'Order questions' });
await conv.run(async (c) => {
  for (const orderId of [7, 9]) {
    const question = `Where is order ${orderId}?`;
    const turn = c.startTurn({ userMessage: question, systemInstructions: ['You are a support agent.'] });
    await turn.run(async (t) => {
      await t.startLLM({ model: 'gpt-4o-mini', providerName: 'openai' }).run((llm) => {
        llm.inputMessages = [weft.Message.user(question)];
        llm.outputMessages = [weft.Message.assistant('', { toolCalls: [{ id: `call-${orderId}`, name: 'lookupOrder', arguments: { orderId } }] })];
        llm.usage = { inputTokens: 42, outputTokens: 12 };
      });
      await t.startTool({ name: 'lookupOrder', arguments: { orderId }, toolCallId: `call-${orderId}` }).run(async (tool) => {
        tool.result = await lookupOrder(orderId);
      });
      t.outputMessages = [weft.Message.assistant(`Order ${orderId} has shipped and arrives Friday.`)];
    });
    turns.push(turn);
  }
});

const ev = new weft.EvaluationLogger({ model: 'support-bot', name: 'support eval (ts)' });
for (const turn of turns) {
  const answer = turn.outputMessages[0].content ?? '';
  const pred = ev.logPrediction({ question: turn.messages[0].content }, answer, { call: turn });
  pred.logScore('mentions_eta', answer.includes('Friday'));
  pred.finish();
}
console.log('eval summary:', await ev.logSummary());

const view = await new weft.SavedView({ label: 'Support tool calls (ts)' }).filterOp('lookupOrder').sortBy('started_at', 'desc').save();
console.log('view', view.name, 'matches', (await view.getCalls()).length, 'calls');
await client.flush();
await weft.finish();
