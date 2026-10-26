import type { MessagesRequest } from './messages.ts'
import type { MessageReply, Script } from './scripts.ts'

// PROJECT.md "Claude Code's own model calls": the model calls Claude Code makes beside a turn, each
// recognized by its prompt and answered here, so no consumer has to script them. They are scripts
// tried after every consumer directory, so a consumer script that matches one of these requests wins.

// A side query's prompt is a system block of its own, apart from a main turn's conversation, so a main
// turn that merely quotes the phrases is never mistaken for the call. Every phrase must be in one block.
function systemBlockHolds(request: MessagesRequest, ...phrases: string[]): boolean {
  return request.systemTexts.some(text => phrases.every(phrase => text.includes(phrase)))
}

function says(text: string, stop_reason: MessageReply['stop_reason']): MessageReply {
  return { kind: 'message', content: [{ type: 'text', text }], stop_reason, eventDelayMs: 0 }
}

// [LAW:one-type-per-behavior] Both phrasings are one call: from 2.1.283 on, "You are naming a coding
// session…", in older builds "Generate a concise, sentence-case title". Both ask, through
// `output_config.format`, for a JSON object with one string field, `title`, and Claude Code parses the
// reply's text as that object.
const TITLE_PROMPTS = [
  'You are naming a coding session so the user can pick it out of a long list of sessions.',
  'Generate a concise, sentence-case title',
]
const SESSION_TITLE = says(JSON.stringify({ title: 'Mock session' }), 'end_turn')

// The classifier's prompt ends in the one answer format it reads, and each format's instruction appears
// only in its own prompts: a severity from 0 to 100 (2.1.289, when the `tengu_auto_mode_config` flag
// asks for it), or `<block>yes|no</block>` (2.1.88, and 2.1.289 otherwise). [LAW:one-source-of-truth]
// The request says which, so the answer is the allowing one in that format: severity 0, the most clearly
// routine, or `no`. [LAW:no-silent-failure] A prompt asking for neither is left unmatched, a 400.
const CLASSIFIER_PROMPT = 'You are a security monitor for autonomous AI coding agents.'
const CLASSIFIER_ALLOWS = [
  { format: '<severity>N</severity>', reply: says('<severity>0</severity>', 'end_turn') },
  { format: '<block>no</block>', reply: says('<block>no</block>', 'end_turn') },
]

// The quota probe asks for one token so Claude Code can read the rate-limit headers; its body is unread.
const QUOTA_PROBE = says('OK', 'max_tokens')

export const BUILT_INS: readonly Script[] = [
  {
    origin: 'built-in: session title',
    reply: request => (TITLE_PROMPTS.some(prompt => systemBlockHolds(request, prompt)) ? SESSION_TITLE : null),
  },
  {
    origin: 'built-in: auto mode classifier',
    reply: request => CLASSIFIER_ALLOWS.find(({ format }) => systemBlockHolds(request, CLASSIFIER_PROMPT, format))?.reply ?? null,
  },
  {
    origin: 'built-in: quota probe',
    reply: request => (request.max_tokens === 1 && request.lastUserTexts.length === 1 && request.lastUserTexts[0] === 'quota' ? QUOTA_PROBE : null),
  },
]
