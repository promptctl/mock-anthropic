import { z } from 'zod'
import { parseJsonBody } from './json-body.ts'

// PROJECT.md "Messages": the one reading of a `POST /v1/messages` body. Every part of the mock that
// answers a Messages request reads the body through this parse, never on its own.

// A content block parses to the texts a script can match in it [LAW:types-are-the-program]: a `text`
// block's text, a `tool_result` block's content, and nothing from any other block, so ids, `type`
// values and image data are never matchable. A block of a type read here that is malformed fails the
// parse rather than passing as some other block.
const TEXT = 'text'
const TOOL_RESULT = 'tool_result'
const READ_TYPES = new Set<string>([TEXT, TOOL_RESULT])
const TextBlock = z.object({ type: z.literal(TEXT), text: z.string() }).transform(block => [block.text])
const OtherBlock = z
  .object({ type: z.string().refine(type => !READ_TYPES.has(type), 'is a block type read here, so must have its fields') })
  .transform((): string[] => [])
// [LAW:one-type-per-behavior] A message's content and a tool result's content are one shape, a
// string or a list of blocks, and differ only in the blocks they admit.
const contentOf = (block: z.ZodType<string[]>) =>
  z.union([z.string().transform(text => [text]), z.array(block).transform(texts => texts.flat())])
const ToolResultBlock = z
  .object({ type: z.literal(TOOL_RESULT), content: contentOf(z.union([TextBlock, OtherBlock])).optional() })
  .transform(block => block.content ?? [])
const Content = contentOf(z.union([TextBlock, ToolResultBlock, OtherBlock]))
const Message = z.object({ role: z.enum(['user', 'assistant']), content: Content })

// The system prompt is a string or a list of text blocks, the only block type the API admits there;
// each block stays one text, so a prompt is recognized by how its own block opens.
const System = z.union([z.string().transform(text => [text]), z.array(z.object({ type: z.literal(TEXT), text: z.string() }).transform(block => block.text))])

// Claude Code sends many fields no part of the mock reads, and zod drops keys a schema does not name,
// so here and in every block above only what is read is declared. A field that a generator reads is
// added here, typed, so the body is still parsed once. `model` and `max_tokens` are required, `stream`
// defaults to false, and `system` to none, as the API has them.
const MessagesBody = z
  .object({
    model: z.string().min(1),
    max_tokens: z.int().positive(),
    stream: z.boolean().default(false),
    system: System.default([]),
    messages: z.array(Message).min(1),
  })
  .transform((body, ctx) => {
    // [LAW:parse-dont-validate] The last user message is a parsed fact: a body with none is not a
    // request a script can be matched against, so it fails here and never reaches one.
    const lastUser = body.messages.findLast(message => message.role === 'user')
    if (lastUser === undefined) {
      ctx.addIssue({ code: 'custom', message: 'has no user message', path: ['messages'] })
      return z.NEVER
    }
    return { model: body.model, max_tokens: body.max_tokens, stream: body.stream, systemTexts: body.system, lastUserTexts: lastUser.content }
  })

// `raw` is the body as sent, for matching anywhere in it; `lastUserTexts` are the texts of the last
// `user` message, the current turn, apart from the same words earlier in the conversation;
// `systemTexts` are the system prompt's blocks; `max_tokens` is the reply's budget; `model` and
// `stream` say what to answer as and how.
export type MessagesRequest = { readonly raw: string } & Readonly<z.output<typeof MessagesBody>>

// A body that is not a Messages request is the generator's to decline, with the reason.
export type ParsedMessages = { readonly kind: 'parsed'; readonly request: MessagesRequest } | { readonly kind: 'invalid'; readonly why: string }

export function parseMessagesRequest(raw: string): ParsedMessages {
  const body = parseJsonBody(raw, MessagesBody, 'a Messages request')
  return body.kind === 'parsed' ? { kind: 'parsed', request: { raw, ...body.value } } : body
}
