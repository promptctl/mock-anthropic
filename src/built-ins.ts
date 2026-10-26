import type { MessagesRequest } from './messages.ts'
import type { MessageReply, Script } from './scripts.ts'

// PROJECT.md "Claude Code's own model calls": the model calls Claude Code makes beside a turn, each
// recognized by its prompt and answered here, so no consumer has to script them. They are scripts
// tried after every consumer directory, so a consumer script that matches one of these requests wins.

// A side query's prompt is its own system block (Claude Code puts its billing header in another), so a
// built-in recognizes the block by how it opens, and a main turn whose conversation merely quotes the
// phrase is never mistaken for the call.
function systemTextOpening(request: MessagesRequest, opening: string): string | undefined {
  return request.systemTexts.find(text => text.startsWith(opening))
}

function says(text: string, stop_reason: MessageReply['stop_reason']): MessageReply {
  return { kind: 'message', content: [{ type: 'text', text }], stop_reason, eventDelayMs: 0 }
}

// [LAW:one-type-per-behavior] Both phrasings are one call: 2.1.283 on, "You are naming a coding
// session…", older builds "Generate a concise, sentence-case title". Both ask, through
// `output_config.format`, for a JSON object with one string field, `title`, and Claude Code parses the
// reply's text as that object.
const TITLE_PROMPTS = [
  'You are naming a coding session so the user can pick it out of a long list of sessions.',
  'Generate a concise, sentence-case title',
]
const SESSION_TITLE = says(JSON.stringify({ title: 'Mock session' }), 'end_turn')

// The classifier's prompt ends in the answer format the `tengu_auto_mode_config` flag chose: a severity
// from 0 to 100, which this answers, or `<block>yes|no</block>`, which it does not. [LAW:no-silent-failure]
// A block-mode request is left unmatched, so it is a 400 and an UNMATCHED line rather than a severity
// Claude Code would read as no decision.
const CLASSIFIER_PROMPT = 'You are a security monitor for autonomous AI coding agents.'
const SEVERITY_FORMAT = '<severity>N</severity>'
// Below 50 allows; 0 is the most clearly routine action.
const ALLOW = says('<severity>0</severity>', 'end_turn')

// The quota probe asks for one token so Claude Code can read the rate-limit headers; its body is unread.
const QUOTA_PROBE = says('OK', 'max_tokens')

export const BUILT_INS: readonly Script[] = [
  {
    origin: 'built-in: session title',
    reply: request => (TITLE_PROMPTS.some(prompt => systemTextOpening(request, prompt) !== undefined) ? SESSION_TITLE : null),
  },
  {
    origin: 'built-in: auto mode classifier',
    reply: request => (systemTextOpening(request, CLASSIFIER_PROMPT)?.includes(SEVERITY_FORMAT) ? ALLOW : null),
  },
  {
    origin: 'built-in: quota probe',
    reply: request => (request.max_tokens === 1 && request.lastUserTexts.length === 1 && request.lastUserTexts[0] === 'quota' ? QUOTA_PROBE : null),
  },
]
