import { createHash } from 'node:crypto'
import { setTimeout as sleep } from 'node:timers/promises'
import type { CountTokensRequest, MessagesRequest } from './messages.ts'
import type { MessageReply, Reply } from './scripts.ts'

// PROJECT.md "Messages": a script's reply as the response the request asked for. A message reply is
// rendered as the Messages API sends an assistant turn, streamed or as one JSON message; a literal
// reply is sent as written, except that one naming no content-type gets the one Bun.serve gives every
// body (text/plain for a string).

type StopReason = MessageReply['stop_reason']

// [LAW:nothing-unseen] What was sent, for the request's record: which form the reply took, and for a
// message its id, the key that joins the record to the consumer's transcript. Built from the same
// values as the response, so the two cannot disagree. Only a stream is paced, so only it has a delay.
export type Rendered =
  | { readonly kind: 'stream'; readonly id: string; readonly stop_reason: StopReason; readonly event_delay_ms: number }
  | { readonly kind: 'json'; readonly id: string; readonly stop_reason: StopReason }
  | { readonly kind: 'literal'; readonly status: number }

export type RenderedReply = { readonly response: Response; readonly rendered: Rendered }

export function renderReply(reply: Reply, request: MessagesRequest): RenderedReply {
  switch (reply.kind) {
    case 'literal':
      return {
        response: new Response(reply.body, { status: reply.status, headers: reply.headers }),
        rendered: { kind: 'literal', status: reply.status },
      }
    case 'message': {
      const message = assistantMessage(reply, request)
      const { id, stop_reason } = message
      return request.stream
        ? { response: eventStream(streamEvents(message), reply.eventDelayMs), rendered: { kind: 'stream', id, stop_reason, event_delay_ms: reply.eventDelayMs } }
        : { response: Response.json(message), rendered: { kind: 'json', id, stop_reason } }
    }
  }
}

// The API's content blocks, as a reply's blocks become once they have ids and signatures.
type ToolInput = Extract<MessageReply['content'][number], { type: 'tool_use' }>['input']
type ContentBlock =
  | { readonly type: 'text'; readonly text: string }
  | { readonly type: 'tool_use'; readonly id: string; readonly name: string; readonly input: ToolInput }
  | { readonly type: 'thinking'; readonly thinking: string; readonly signature: string }

type Usage = {
  readonly input_tokens: number
  readonly cache_creation_input_tokens: number
  readonly cache_read_input_tokens: number
  readonly output_tokens: number
}

type AssistantMessage = {
  readonly id: string
  readonly type: 'message'
  readonly role: 'assistant'
  readonly model: string
  readonly content: readonly ContentBlock[]
  readonly stop_reason: StopReason
  readonly stop_sequence: null
  readonly usage: Usage
}

// [LAW:types-are-the-program] Every id is a function of the raw body alone: the same request gets the
// same ids in every run, and different requests get different ones, with no counter to keep. The body
// is hashed once; each block's id hashes that digest with the block's type and index, joined by NUL,
// which none of them holds, so no two part lists hash alike.
function digest(...parts: string[]): string {
  return createHash('sha256').update(parts.join('\0')).digest('hex')
}

const ID_HEX = 24

function assistantMessage(reply: MessageReply, request: MessagesRequest): AssistantMessage {
  const requestDigest = digest(request.raw)
  const content = reply.content.map((block, index): ContentBlock => {
    switch (block.type) {
      case 'text':
        return block
      case 'tool_use':
        return { type: 'tool_use', id: `toolu_${digest(requestDigest, 'tool_use', String(index)).slice(0, ID_HEX)}`, name: block.name, input: block.input }
      case 'thinking':
        return { type: 'thinking', thinking: block.thinking, signature: Buffer.from(digest(requestDigest, 'thinking', String(index)), 'hex').toString('base64') }
    }
  })
  return {
    id: `msg_${requestDigest.slice(0, ID_HEX)}`,
    type: 'message',
    role: 'assistant',
    model: request.model,
    content,
    stop_reason: reply.stop_reason,
    stop_sequence: null,
    usage: {
      input_tokens: inputTokens(request),
      cache_creation_input_tokens: 0,
      cache_read_input_tokens: 0,
      output_tokens: Math.max(1, content.reduce((sum, block) => sum + tokens(streamedText(block)), 0)),
    },
  }
}

// Plausible, not counted: about four characters to a token, as English text runs.
function tokens(text: string): number {
  return Math.ceil(text.length / 4)
}

// [LAW:one-source-of-truth] A request's input tokens, the one count both a Messages reply's usage and
// a token count answer give, so the two agree on the same request.
export function inputTokens(request: MessagesRequest | CountTokensRequest): number {
  return tokens(request.raw)
}

// A server-sent event of the Messages stream: its `type` is also its SSE event name.
type StreamEvent = { readonly type: string } & Readonly<Record<string, unknown>>

// The text a block streams, which its output tokens are counted from.
function streamedText(block: ContentBlock): string {
  switch (block.type) {
    case 'text':
      return block.text
    case 'tool_use':
      return JSON.stringify(block.input)
    case 'thinking':
      return block.thinking
  }
}

// What a block streams: the block as content_block_start opens it, with its streamed field empty, and
// the deltas that fill that field in. A thinking block's signature arrives in one signature_delta
// after its text, as the API sends it.
function streamed(block: ContentBlock): { start: ContentBlock; deltas: StreamEvent[] } {
  const parts = chunks(streamedText(block))
  switch (block.type) {
    case 'text':
      return { start: { ...block, text: '' }, deltas: parts.map(text => ({ type: 'text_delta', text })) }
    case 'tool_use':
      return { start: { ...block, input: {} }, deltas: parts.map(partial_json => ({ type: 'input_json_delta', partial_json })) }
    case 'thinking':
      return {
        start: { ...block, thinking: '', signature: '' },
        deltas: [...parts.map(thinking => ({ type: 'thinking_delta', thinking })), { type: 'signature_delta', signature: block.signature }],
      }
  }
}

// [LAW:one-source-of-truth] The stream is derived from the message the JSON reply would be, so a
// client that accumulates the deltas builds that same message. The events and their order are the API's.
function streamEvents(message: AssistantMessage): readonly [StreamEvent, ...StreamEvent[]] {
  const { content, usage, stop_reason, stop_sequence } = message
  return [
    { type: 'message_start', message: { ...message, content: [], stop_reason: null, usage: { ...usage, output_tokens: 1 } } },
    ...content.flatMap((block, index) => {
      const { start, deltas } = streamed(block)
      return [
        { type: 'content_block_start', index, content_block: start },
        ...deltas.map(delta => ({ type: 'content_block_delta', index, delta })),
        { type: 'content_block_stop', index },
      ]
    }),
    { type: 'message_delta', delta: { stop_reason, stop_sequence }, usage: { output_tokens: usage.output_tokens } },
    { type: 'message_stop' },
  ]
}

// Code points per delta: enough deltas that a short reply visibly streams. Split by code point, so no
// delta ends in half a surrogate pair.
const CHUNK = 16

function chunks(text: string): string[] {
  const points = Array.from(text)
  return Array.from({ length: Math.ceil(points.length / CHUNK) }, (_, i) => points.slice(i * CHUNK, (i + 1) * CHUNK).join(''))
}

const EVENT_STREAM_HEADERS = { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache' }

// [LAW:dataflow-not-control-flow] Every stream is paced the same way: each event after the first
// awaits one wait. The wait is chosen once, from the delay: a delay of 0 is no wait at all, since even
// a 0 ms timer costs Bun about 1.5 ms, which across a long reply's thousands of deltas is seconds the
// script never asked for. [LAW:no-ambient-temporal-coupling] The stream owns its timer: a client that
// leaves cancels the stream, and the cancel aborts the wait in progress.
function eventStream(events: readonly [StreamEvent, ...StreamEvent[]], delayMs: number): Response {
  const encoder = new TextEncoder()
  const frame = (event: StreamEvent) => encoder.encode(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`)
  const cancelled = new AbortController()
  const frames = paced(events, delayMs, cancelled.signal)
  const body = new ReadableStream<Uint8Array>({
    async pull(controller) {
      const next = await frames.next()
      if (next.done) controller.close()
      else controller.enqueue(frame(next.value))
    },
    cancel(reason) {
      cancelled.abort(reason)
    },
  })
  return new Response(body, { headers: EVENT_STREAM_HEADERS })
}

async function* paced(events: readonly [StreamEvent, ...StreamEvent[]], delayMs: number, signal: AbortSignal): AsyncGenerator<StreamEvent> {
  const wait = delayMs === 0 ? () => Promise.resolve() : () => sleep(delayMs, undefined, { signal })
  const [first, ...rest] = events
  yield first
  for (const event of rest) {
    await wait()
    yield event
  }
}
