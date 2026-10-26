import { z } from 'zod'

// PROJECT.md "Messages": the one reading of a `POST /v1/messages` body. Every part of the mock that
// answers a Messages request reads the body through this parse, never on its own.

// A content block parses to the texts a script can match in it [LAW:types-are-the-program]: a `text`
// block's text, a `tool_result` block's content, and nothing from any other block, so ids, `type`
// values and image data are never matchable. A block of a type read here that is malformed fails the
// parse rather than passing as some other block.
const TEXT = 'text'
const TOOL_RESULT = 'tool_result'
const READ_TYPES = new Set<string>([TEXT, TOOL_RESULT])
const TextBlock = z.looseObject({ type: z.literal(TEXT), text: z.string() }).transform(block => [block.text])
const OtherBlock = z
  .looseObject({ type: z.string().refine(type => !READ_TYPES.has(type), 'is a block type read here, so must have its fields') })
  .transform((): string[] => [])
const ToolResultContent = z.union([
  z.string().transform(text => [text]),
  z.array(z.union([TextBlock, OtherBlock])).transform(texts => texts.flat()),
])
const ToolResultBlock = z
  .looseObject({ type: z.literal(TOOL_RESULT), content: ToolResultContent.optional() })
  .transform(block => block.content ?? [])
const Content = z.union([
  z.string().transform(text => [text]),
  z.array(z.union([TextBlock, ToolResultBlock, OtherBlock])).transform(texts => texts.flat()),
])
const Message = z.looseObject({ role: z.enum(['user', 'assistant']), content: Content })

// Loose, because Claude Code sends many fields no part of the mock reads; a field that a generator
// reads is added here, typed, so the body is still parsed once.
const MessagesBody = z.looseObject({ messages: z.array(Message).min(1) }).transform((body, ctx) => {
  // [LAW:parse-dont-validate] The last user message is a parsed fact: a body with none is not a
  // request a script can be matched against, so it fails here and never reaches one.
  const lastUser = body.messages.findLast(message => message.role === 'user')
  if (lastUser === undefined) {
    ctx.addIssue({ code: 'custom', message: 'has no user message', path: ['messages'] })
    return z.NEVER
  }
  return { lastUserTexts: lastUser.content }
})

// `raw` is the body as sent, for matching anywhere in it; `lastUserTexts` are the texts of the last
// `user` message, the current turn, apart from the same words earlier in the conversation.
export type MessagesRequest = { readonly raw: string } & Readonly<z.output<typeof MessagesBody>>

// A body that is not a Messages request is the generator's to decline, with the reason.
export type ParsedMessages = { readonly kind: 'parsed'; readonly request: MessagesRequest } | { readonly kind: 'invalid'; readonly why: string }

export function parseMessagesRequest(raw: string): ParsedMessages {
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch (error) {
    return { kind: 'invalid', why: `body is not JSON: ${String(error)}` }
  }
  const result = MessagesBody.safeParse(value)
  return result.success
    ? { kind: 'parsed', request: { raw, ...result.data } }
    : { kind: 'invalid', why: `body is not a Messages request: ${z.prettifyError(result.error)}` }
}
