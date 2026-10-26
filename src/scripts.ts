import { lstat, readdir } from 'node:fs/promises'
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
// [LAW:parse-dont-validate] A literal reply is one a Response can carry, checked when the script is
// read so the error names the script: the Fetch standard's own Headers check, and an empty body on a
// status that has none (RFC 9110 15.3.5, 15.3.6, 15.4.5).
const NULL_BODY_STATUSES = new Set([204, 205, 304])
const LiteralReply = z
  .strictObject({
    kind: z.literal('literal'),
    status: z.int().min(200).max(599),
    headers: z.record(z.string(), z.string()).default({}).check(ctx => {
      try {
        new Headers(ctx.value)
      } catch (error) {
        ctx.issues.push({ code: 'custom', message: String(error), input: ctx.value })
      }
    }),
    body: z.string(),
  })
  .refine(reply => reply.body === '' || !NULL_BODY_STATUSES.has(reply.status), {
    message: 'a 204, 205 or 304 has no body',
    path: ['body'],
  })
const ReplySchema = z.discriminatedUnion('kind', [MessageReply, LiteralReply])

export type Reply = z.output<typeof ReplySchema>
export type MessageReply = Extract<Reply, { kind: 'message' }>
export type LiteralReply = Extract<Reply, { kind: 'literal' }>

// Every needle must appear: `bodyContains` anywhere in the raw body (the JSON text as sent, so a
// needle can name a field), `lastUserContains` within one of the last user message's texts. An empty needle would match everything, so it is refused; a script with no
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
// entries in code-unit order of their names, which no locale changes. A hidden name (an editor's lock
// file, macOS's `._` metadata) or any other suffix is not a script, so a consumer that writes
// `x.json.tmp` and renames it never has a half-written script read.
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
  const paths = names.filter(name => name.endsWith(SCRIPT_SUFFIX) && !name.startsWith('.')).sort().map(name => join(dir, name))
  const scripts = await Promise.all(paths.map(readScript))
  return scripts.flat()
}

// A script removed after its directory was listed is not in the directory any more, so it reads as
// none; a name that is still there and cannot be read, such as a dangling symlink, throws.
async function readScript(path: string): Promise<Script[]> {
  let text: string
  try {
    text = await Bun.file(path).text()
  } catch (error) {
    if (await removed(path)) return []
    throw new Error(`script ${path}: cannot read: ${String(error)}`, { cause: error })
  }
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch (error) {
    throw new Error(`script ${path}: not JSON: ${String(error)}`, { cause: error })
  }
  const result = ScriptFile.safeParse(value)
  if (!result.success) throw new Error(`script ${path}: ${z.prettifyError(result.error)}`)
  const { match, reply } = result.data
  return [
    {
      origin: path,
      reply: request =>
        match.bodyContains.every(needle => request.raw.includes(needle)) &&
        match.lastUserContains.every(needle => request.lastUserTexts.some(text => text.includes(needle)))
          ? reply
          : null,
    },
  ]
}

async function removed(path: string): Promise<boolean> {
  return lstat(path).then(
    () => false,
    (error: NodeJS.ErrnoException) => error.code === 'ENOENT',
  )
}
