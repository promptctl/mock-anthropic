import type { Answer, MockRequest } from './dispatch.ts'
import type { ResolutionOf } from './endpoints.ts'
import { parseMessagesRequest } from './messages.ts'
import { renderReply } from './render.ts'
import { type Script, selectReply } from './scripts.ts'

// PROJECT.md "Messages": the generator for `POST /v1/messages`. [LAW:effects-at-boundaries] The one
// place a Messages request meets the scripts: it parses the body, reads the consumer's directories
// (the effect), and renders the first reply that answers.
// A script that cannot be read or is not a script throws out of selectReply, and dispatch turns that
// into the 500 and FAILED line naming its path (PROJECT.md "A bad script").
export function messagesGenerator(dirs: readonly string[], builtIns: readonly Script[]) {
  return async (_: ResolutionOf<'messages'>, request: MockRequest): Promise<Answer> => {
    const parsed = parseMessagesRequest(request.body)
    // No script was consulted, so there is nothing to report beyond the why.
    if (parsed.kind === 'invalid') return { kind: 'declined', why: parsed.why, facts: {} }
    const selection = await selectReply(dirs, builtIns, parsed.request)
    // [LAW:nothing-unseen] Each answer is built with its facts: the script that answered and what was
    // sent, or every script tried. "0 tried" is a request that found no scripts at all.
    switch (selection.kind) {
      case 'matched': {
        const { response, rendered } = renderReply(selection.reply, parsed.request)
        return { kind: 'response', response, facts: { script: selection.origin, reply: rendered } }
      }
      case 'unmatched':
        return { kind: 'declined', why: `no script matches: ${selection.tried.length} tried`, facts: { scripts_tried: selection.tried } }
    }
  }
}
