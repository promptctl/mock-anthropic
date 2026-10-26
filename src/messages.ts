import { z } from 'zod'
import { type ParsedBody, parseJsonBody } from './json-body.ts'

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
// `system` is a role Claude Code sends in `messages` (2.1.289 puts a hook's added context there),
// though the API's own documentation names only `user` and `assistant`. Its texts are the
// conversation's, not the system prompt's, so they never reach `systemTexts`.
const Message = z.object({ role: z.enum(['user', 'assistant', 'system']), content: Content })

// The system prompt is a string or a list of text blocks, the only block type the API admits there;
// each block stays one text, so a prompt is recognized by how its own block opens.
const System = z.union([z.string().transform(text => [text]), z.array(z.object({ type: z.literal(TEXT), text: z.string() }).transform(block => block.text))])

// Claude Code sends many fields no part of the mock reads, and zod drops keys a schema does not name,
// so here and in every block above only what is read is declared. A field that a generator reads is
// added here, typed, so the body is still parsed once. `model` is required, and `system` defaults to
// none, as the API has them. [LAW:one-source-of-truth] The base is what a Messages request and a
// token count request share, so the two endpoints refuse the same messages for the same reason.
const RequestBase = z.object({ model: z.string().min(1), system: System.default([]), messages: z.array(Message).min(1) })

// Plausible, not counted: about four characters to a token, as English text runs.
export function tokens(text: string): number {
  return Math.ceil(text.length / 4)
}

// [LAW:one-source-of-truth] A request's input tokens, the one count both a token count answer and a
// Messages reply's usage give: its system prompt, messages and tools as sent, and none of the fields
// that shape only the reply (`max_tokens`, `stream`, `metadata`), so the two agree on one conversation.
const InputTokens = z
  .object({ system: z.unknown().optional(), messages: z.unknown(), tools: z.unknown().optional() })
  .transform(({ system, messages, tools }) => ({ inputTokens: tokens(JSON.stringify([system, messages, tools])) }))

// `max_tokens` is required and `stream` defaults to false, as the API has them.
const MessagesBody = RequestBase.extend({ max_tokens: z.int().positive(), stream: z.boolean().default(false) })
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
  .and(InputTokens)

declare const parsed: unique symbol

// [LAW:parse-dont-validate] A request body this module parsed. The brand is a type-level stamp with no
// runtime value, applied only by the parses below, so nothing else can pass for a parsed request.
type Parsed = { readonly [parsed]: true }

// `raw` is the body as sent, for matching anywhere in it; `inputTokens` is what it counts as;
// `lastUserTexts` are the texts of the last `user` message, the current turn, apart from the same
// words earlier in the conversation; `systemTexts` are the system prompt's blocks; `max_tokens` is the
// reply's budget; `model` and `stream` say what to answer as and how.
export type MessagesRequest = Parsed & { readonly raw: string } & Readonly<z.output<typeof MessagesBody>>

// A body that is not a Messages request is the generator's to decline, with the reason.
export function parseMessagesRequest(raw: string): ParsedBody<MessagesRequest> {
  const body = parseJsonBody(raw, MessagesBody, 'a Messages request')
  return body.kind === 'parsed' ? { kind: 'parsed', value: stamp({ raw, ...body.value }) } : body
}

// `POST /v1/messages/count_tokens`: the base alone. No script is matched against it, so it needs no
// last user message; its `inputTokens` are counted as a Messages request's are.
export type CountTokensRequest = Parsed & { readonly inputTokens: number }

export function parseCountTokensRequest(raw: string): ParsedBody<CountTokensRequest> {
  const body = parseJsonBody(raw, RequestBase.and(InputTokens), 'a token count request')
  return body.kind === 'parsed' ? { kind: 'parsed', value: stamp({ inputTokens: body.value.inputTokens }) } : body
}

// The one place the brand is applied, to a value a parse above just produced.
const stamp = <T>(value: T) => value as T & Parsed
