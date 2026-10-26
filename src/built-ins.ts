import type { MessagesRequest } from './messages.ts'
import { parseReply, type Reply, type Script } from './scripts.ts'

// PROJECT.md "Claude Code's own model calls": the model calls Claude Code makes beside a turn, each
// recognized by its prompt and answered here, so no consumer has to script them. They are scripts
// tried after every consumer directory, so a consumer script that matches one of these requests wins.

// The system block a side query's prompt opens. Claude Code gives the billing header and its identity
// line blocks of their own and joins the rest into one, so a side query's prompt opens its block, while
// a main turn's block opens with Claude Code's own prompt, whatever it quotes.
function promptBlock(request: MessagesRequest, opening: string): string | undefined {
  return request.systemTexts.find(text => text.startsWith(opening))
}

const says = (text: string, stop_reason: 'end_turn' | 'max_tokens'): Reply =>
  parseReply({ kind: 'message', content: [{ type: 'text', text }], stop_reason })

// [LAW:one-type-per-behavior] Both phrasings are one call: from 2.1.283 on, "You are naming a coding
// session…", in older builds "Generate a concise, sentence-case title". Both ask, through
// `output_config.format`, for a JSON object with one string field, `title`, and Claude Code parses the
// reply's text as that object.
const TITLE_PROMPTS = [
  'You are naming a coding session so the user can pick it out of a long list of sessions.',
  'Generate a concise, sentence-case title',
]
const SESSION_TITLE = says(JSON.stringify({ title: 'Mock session' }), 'end_turn')

// The classifier's prompt names the one answer format it reads, and each format's instruction appears
// only in its own prompts: a severity from 0 to 100 (2.1.289, when the `tengu_auto_mode_config` flag
// asks for it), `<block>yes|no</block>` (2.1.289 otherwise, and 2.1.88 when the flag's
// `twoStageClassifier` is set), or a call to the `classify_result` tool (2.1.88 otherwise).
// [LAW:one-source-of-truth] The request says which, so the answer is the allowing one in that format.
// [LAW:nothing-unseen] Each format is its own built-in, so the record names the answer that was sent.
// [LAW:no-silent-failure] A prompt asking for none of them is left unmatched, a 400.
const CLASSIFIER_PROMPT = 'You are a security monitor for autonomous AI coding agents.'
const CLASSIFIER_ALLOWS = [
  { format: 'severity', asks: '<severity>N</severity>', reply: says('<severity>0</severity>', 'end_turn') },
  { format: 'block', asks: '<block>no</block>', reply: says('<block>no</block>', 'end_turn') },
  {
    format: 'classify_result tool',
    asks: 'Use the classify_result tool to report your classification.',
    reply: parseReply({
      kind: 'message',
      content: [{ type: 'tool_use', name: 'classify_result', input: { thinking: '', shouldBlock: false, reason: 'Allowed by default.' } }],
      stop_reason: 'tool_use',
    }),
  },
]

// The quota probe asks for one token so Claude Code can read the rate-limit headers; its body is unread.
const QUOTA_PROBE = says('OK', 'max_tokens')

export const BUILT_INS: readonly Script[] = [
  {
    origin: 'built-in: session title',
    reply: request => (TITLE_PROMPTS.some(prompt => promptBlock(request, prompt) !== undefined) ? SESSION_TITLE : null),
  },
  ...CLASSIFIER_ALLOWS.map(({ format, asks, reply }): Script => ({
    origin: `built-in: auto mode classifier, ${format}`,
    reply: request => (promptBlock(request, CLASSIFIER_PROMPT)?.includes(asks) ? reply : null),
  })),
  {
    origin: 'built-in: quota probe',
    reply: request => (request.max_tokens === 1 && request.lastUserTexts.length === 1 && request.lastUserTexts[0] === 'quota' ? QUOTA_PROBE : null),
  },
]
