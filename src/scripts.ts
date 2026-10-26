import { readdir } from 'node:fs/promises'
import { join } from 'node:path'
import { z } from 'zod'
import type { MessagesRequest } from './messages.ts'

// PROJECT.md "Scripts of model replies": which reply answers a Messages request. A consumer declares
// scripts as files in directories; the mock's built-in answers are scripts written in code. Both are
// tried in one order, and the first that answers wins.

// [LAW:domain-language] What mirrors the Messages API is spelled as the API spells it (`content`,
// `stop_reason`, the block fields); the mock's own concepts take PROJECT.md's names. Strict
// throughout, so a misspelled key is rejected by name instead of ignored.
const ContentBlock = z.discriminatedUnion('type', [
  z.strictObject({ type: z.literal('text'), text: z.string() }),
  z.strictObject({ type: z.literal('tool_use'), name: z.string().min(1), input: z.record(z.string(), z.json()) }),
  z.strictObject({ type: z.literal('thinking'), thinking: z.string() }),
])

// [LAW:types-are-the-program] A reply is an assistant message the mock renders as the request asks
// (stream or JSON), or a literal response sent as written, so a delay on a literal or a status on a
// message cannot be written. The stop reason agrees with the blocks as the API's replies do: a turn
// ends on `tool_use` exactly when it holds a tool call, and one cut off at `max_tokens` may hold one or not.
const MessageReply = z
  .strictObject({
    kind: z.literal('message'),
    content: z.array(ContentBlock).min(1),
    stop_reason: z.enum(['end_turn', 'tool_use', 'max_tokens']),
    eventDelayMs: z.int().nonnegative().default(0),
  })
  .refine(
    reply => reply.stop_reason === 'max_tokens' || (reply.stop_reason === 'tool_use') === reply.content.some(block => block.type === 'tool_use'),
    { message: 'stop_reason "tool_use" needs a tool_use block, and "end_turn" allows none', path: ['stop_reason'] },
  )
const LiteralReply = z.strictObject({
  kind: z.literal('literal'),
  status: z.int().min(200).max(599),
  headers: z.record(z.string(), z.string()).default({}),
  body: z.string(),
})
const ReplySchema = z.discriminatedUnion('kind', [MessageReply, LiteralReply])

export type Reply = z.output<typeof ReplySchema>
export type MessageReply = Extract<Reply, { kind: 'message' }>
export type LiteralReply = Extract<Reply, { kind: 'literal' }>

// Every needle must appear: `bodyContains` anywhere in the raw body, `lastUserContains` in the last
// user message's text. An empty needle would match everything, so it is refused; a script with no
// needles answers every Messages request.
const Needles = z.array(z.string().min(1)).default([])
const ScriptFile = z.strictObject({
  match: z.strictObject({ bodyContains: Needles, lastUserContains: Needles }).prefault({}),
  reply: ReplySchema,
})

// [LAW:one-type-per-behavior] A consumer's file and a built-in answer are one thing: a named source
// that answers a request or passes. `origin` is a file's path or a built-in's name.
export type Script = { readonly origin: string; reply(request: MessagesRequest): Reply | null }

// [LAW:nothing-unseen] Which script answered, or every script tried, so a request no script answered
// can be told apart from one that found no scripts at all.
export type Selection =
  | { readonly kind: 'matched'; readonly origin: string; readonly reply: Reply }
  | { readonly kind: 'unmatched'; readonly tried: readonly string[] }

export function firstReply(scripts: readonly Script[], request: MessagesRequest): Selection {
  for (const script of scripts) {
    const reply = script.reply(request)
    if (reply !== null) return { kind: 'matched', origin: script.origin, reply }
  }
  return { kind: 'unmatched', tried: scripts.map(script => script.origin) }
}

// [LAW:one-source-of-truth] The one place the order is written: consumer directories in the order
// given, then the built-ins. Directories are read on every call, so a script written after the mock
// started answers the next request.
export async function selectReply(dirs: readonly string[], builtIns: readonly Script[], request: MessagesRequest): Promise<Selection> {
  return firstReply([...(await readScripts(dirs)), ...builtIns], request)
}

const SCRIPT_SUFFIX = '.json'

// [LAW:effects-at-boundaries] The one read of the consumer's directories: each directory's `.json`
// entries in code-unit order of their names, which no locale changes. Any other name is not a script,
// so a consumer that writes `x.json.tmp` and renames it never has a half-written script read.
// [LAW:no-silent-failure] An unreadable directory or file, or a file that is not a script, throws
// with its path; the generator's caller turns that into a 500 and a FAILED line.
export async function readScripts(dirs: readonly string[]): Promise<Script[]> {
  const perDir = await Promise.all(dirs.map(readDirectory))
  return perDir.flat()
}

async function readDirectory(dir: string): Promise<Script[]> {
  const names = await readdir(dir).catch((error: unknown) => {
    throw new Error(`scripts directory ${dir}: cannot read: ${String(error)}`, { cause: error })
  })
  const paths = names.filter(name => name.endsWith(SCRIPT_SUFFIX)).sort().map(name => join(dir, name))
  return Promise.all(paths.map(readScript))
}

async function readScript(path: string): Promise<Script> {
  const text = await Bun.file(path).text().catch((error: unknown) => {
    throw new Error(`script ${path}: cannot read: ${String(error)}`, { cause: error })
  })
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch (error) {
    throw new Error(`script ${path}: not JSON: ${String(error)}`, { cause: error })
  }
  const result = ScriptFile.safeParse(value)
  if (!result.success) throw new Error(`script ${path}: ${z.prettifyError(result.error)}`)
  const { match, reply } = result.data
  return {
    origin: path,
    reply: request =>
      match.bodyContains.every(needle => request.raw.includes(needle)) &&
      match.lastUserContains.every(needle => request.lastUserText.includes(needle))
        ? reply
        : null,
  }
}
