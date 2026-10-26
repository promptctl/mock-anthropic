import { describe, expect, test } from 'bun:test'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { type MessagesRequest, parseMessagesRequest } from './messages.ts'
import { renderReply } from './render.ts'
import { type MessageReply, type Reply, readScripts } from './scripts.ts'

const MODEL = 'claude-sonnet-5-5'

function request(text: string, stream: boolean): MessagesRequest {
  const result = parseMessagesRequest(JSON.stringify({ model: MODEL, max_tokens: 1024, stream, messages: [{ role: 'user', content: text }] }))
  if (result.kind !== 'parsed') throw new Error(result.why)
  return result.value
}

function message(content: MessageReply['content'], stop_reason: MessageReply['stop_reason'], eventDelayMs = 0): MessageReply {
  return { kind: 'message', content, stop_reason, eventDelayMs }
}

type Event = { type: string; [key: string]: unknown }

// The fields of a JSON reply the tests read.
type Body = { id: string; content: ({ id?: string } & Record<string, unknown>)[]; usage: { input_tokens: number; output_tokens: number } & Record<string, unknown> } & Record<string, unknown>

function json(response: Response): Promise<Body> {
  return response.json() as Promise<Body>
}

// Reads a text/event-stream body as a client does: events separated by a blank line, each an `event:`
// line naming it and a `data:` line holding it as JSON. Every event's name is its data's `type`.
async function events(response: Response): Promise<Event[]> {
  expect(response.headers.get('content-type')).toBe('text/event-stream; charset=utf-8')
  const text = await response.text()
  expect(text.endsWith('\n\n')).toBe(true)
  return text
    .slice(0, -2)
    .split('\n\n')
    .map(block => {
      const [eventLine, dataLine, ...extra] = block.split('\n')
      expect(extra).toEqual([])
      expect(eventLine).toStartWith('event: ')
      expect(dataLine).toStartWith('data: ')
      const event = JSON.parse(dataLine!.slice('data: '.length)) as Event
      expect(eventLine).toBe(`event: ${event.type}`)
      return event
    })
}

// Accumulates a stream into the message it describes, as the Anthropic SDK does: message_start opens
// the message, each block starts and its deltas fill it in, message_delta sets the stop reason and the
// output tokens.
function accumulate(stream: Event[]): Record<string, unknown> {
  let message: Record<string, unknown> = {}
  const content: Record<string, unknown>[] = []
  const json: string[] = []
  for (const event of stream) {
    if (event.type === 'message_start') message = { ...(event.message as object) }
    if (event.type === 'content_block_start') {
      content[event.index as number] = { ...(event.content_block as object) }
      json[event.index as number] = ''
    }
    if (event.type === 'content_block_delta') {
      const block = content[event.index as number]!
      const delta = event.delta as Record<string, string>
      if (delta.type === 'text_delta') block.text = `${block.text as string}${delta.text}`
      if (delta.type === 'thinking_delta') block.thinking = `${block.thinking as string}${delta.thinking}`
      if (delta.type === 'signature_delta') block.signature = delta.signature
      if (delta.type === 'input_json_delta') json[event.index as number] += delta.partial_json!
    }
    if (event.type === 'content_block_stop' && content[event.index as number]!.type === 'tool_use') {
      content[event.index as number]!.input = JSON.parse(json[event.index as number]!)
    }
    if (event.type === 'message_delta') {
      message = { ...message, ...(event.delta as object), usage: { ...(message.usage as object), ...(event.usage as object) } }
    }
  }
  return { ...message, content }
}

function types(stream: Event[]): string[] {
  return stream.map(event => (event.type === 'content_block_delta' ? `delta:${(event.delta as Event).type}` : event.type))
}

const TOOL_INPUT = { command: 'ls -la /tmp', description: 'List the temporary directory' }

describe('a streamed message', () => {
  test('a text block: message_start, the block and its text deltas, message_delta with the stop reason and usage, message_stop', async () => {
    const text = 'Hello there, this text is long enough to arrive in more than one delta.'
    const stream = await events(renderReply(message([{ type: 'text', text }], 'end_turn'), request('hi', true)).response)
    const deltas = stream.filter(event => event.type === 'content_block_delta')
    expect(deltas.length).toBeGreaterThan(1)
    expect(types(stream)).toEqual(['message_start', 'content_block_start', ...deltas.map(() => 'delta:text_delta'), 'content_block_stop', 'message_delta', 'message_stop'])
    expect(stream[0]).toMatchObject({ message: { type: 'message', role: 'assistant', model: MODEL, content: [], stop_reason: null } })
    expect(stream[1]).toEqual({ type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } })
    expect(deltas.map(event => (event.delta as { text: string }).text).join('')).toBe(text)
    const end = stream.at(-2)!
    expect(end.delta).toEqual({ stop_reason: 'end_turn', stop_sequence: null })
    expect((end.usage as { output_tokens: number }).output_tokens).toBeGreaterThan(1)
  })

  test('a tool_use block: an id, the name, and input_json_delta chunks that parse to the input', async () => {
    const stream = await events(renderReply(message([{ type: 'tool_use', name: 'Bash', input: TOOL_INPUT }], 'tool_use'), request('list it', true)).response)
    const deltas = stream.filter(event => event.type === 'content_block_delta')
    expect(types(stream)).toEqual(['message_start', 'content_block_start', ...deltas.map(() => 'delta:input_json_delta'), 'content_block_stop', 'message_delta', 'message_stop'])
    expect(stream[1]).toEqual({ type: 'content_block_start', index: 0, content_block: { type: 'tool_use', id: expect.stringMatching(/^toolu_[0-9a-f]{24}$/), name: 'Bash', input: {} } })
    expect(JSON.parse(deltas.map(event => (event.delta as { partial_json: string }).partial_json).join(''))).toEqual(TOOL_INPUT)
    expect(stream.at(-2)!.delta).toEqual({ stop_reason: 'tool_use', stop_sequence: null })
  })

  test('a thinking block: thinking deltas, then its signature in one signature_delta', async () => {
    const thinking = 'The user wants a listing; the Bash tool can do that.'
    const stream = await events(renderReply(message([{ type: 'thinking', thinking }], 'max_tokens'), request('think', true)).response)
    const deltas = stream.filter(event => event.type === 'content_block_delta').map(event => event.delta as Record<string, string>)
    expect(types(stream)).toEqual([
      'message_start',
      'content_block_start',
      ...deltas.slice(0, -1).map(() => 'delta:thinking_delta'),
      'delta:signature_delta',
      'content_block_stop',
      'message_delta',
      'message_stop',
    ])
    expect(stream[1]).toEqual({ type: 'content_block_start', index: 0, content_block: { type: 'thinking', thinking: '', signature: '' } })
    expect(deltas.slice(0, -1).map(delta => delta.thinking).join('')).toBe(thinking)
    expect(deltas.at(-1)!.signature).not.toBe('')
  })

  test('accumulates to the message the same request gets as JSON, block by block in order', async () => {
    const reply = message(
      [
        { type: 'thinking', thinking: 'Listing first.' },
        { type: 'text', text: 'Let me look 👀 at the directory.' },
        { type: 'tool_use', name: 'Bash', input: TOOL_INPUT },
        { type: 'tool_use', name: 'Read', input: {} },
      ],
      'tool_use',
    )
    const streaming = request('look', true)
    const stream = await events(renderReply(reply, streaming).response)
    expect(stream.filter(event => event.type === 'content_block_start').map(event => event.index)).toEqual([0, 1, 2, 3])
    expect(accumulate(stream)).toEqual(await json(renderReply(reply, { ...streaming, stream: false }).response))
  })

  test('splits text by code point, so no delta holds half a surrogate pair', async () => {
    const text = '🙂'.repeat(40)
    const stream = await events(renderReply(message([{ type: 'text', text }], 'end_turn'), request('smile', true)).response)
    const texts = stream.filter(event => event.type === 'content_block_delta').map(event => (event.delta as { text: string }).text)
    expect(texts.every(chunk => chunk.isWellFormed())).toBe(true)
    expect(texts.join('')).toBe(text)
  })
})

describe('a JSON message', () => {
  test('is one assistant message with ids, the requested model, the stop reason and plausible usage', async () => {
    const sent = request('hello', false)
    const { response, rendered } = renderReply(message([{ type: 'text', text: 'Hi.' }, { type: 'tool_use', name: 'Bash', input: TOOL_INPUT }], 'tool_use'), sent)
    expect(response.headers.get('content-type')).toStartWith('application/json')
    const body = await json(response)
    expect(body).toEqual({
      id: expect.stringMatching(/^msg_[0-9a-f]{24}$/),
      type: 'message',
      role: 'assistant',
      model: MODEL,
      content: [
        { type: 'text', text: 'Hi.' },
        { type: 'tool_use', id: expect.stringMatching(/^toolu_[0-9a-f]{24}$/), name: 'Bash', input: TOOL_INPUT },
      ],
      stop_reason: 'tool_use',
      stop_sequence: null,
      usage: { input_tokens: expect.any(Number), cache_creation_input_tokens: 0, cache_read_input_tokens: 0, output_tokens: expect.any(Number) },
    })
    expect(body.usage.input_tokens).toBe(sent.inputTokens)
    expect(body.usage.output_tokens).toBeGreaterThan(1)
    expect(rendered).toEqual({ kind: 'json', id: body.id, stop_reason: 'tool_use' })
  })

  test('an empty text reply still reports an output token', async () => {
    const body = await json(renderReply(message([{ type: 'text', text: '' }], 'end_turn'), request('say nothing', false)).response)
    expect(body.usage.output_tokens).toBe(1)
  })
})

describe('ids', () => {
  const TWO_TOOLS = message(
    [
      { type: 'tool_use', name: 'Bash', input: TOOL_INPUT },
      { type: 'tool_use', name: 'Bash', input: TOOL_INPUT },
    ],
    'tool_use',
  )

  async function ids(sent: MessagesRequest): Promise<string[]> {
    const body = await json(renderReply(TWO_TOOLS, sent).response)
    return [body.id, ...body.content.map(block => block.id!)]
  }

  test('the same request gets the same ids in another run of the mock', async () => {
    const sent = request('same every run', false)
    const script = `
      import { renderReply } from ${JSON.stringify(join(import.meta.dir, 'render.ts'))}
      const [reply, sent] = JSON.parse(await Bun.stdin.text())
      const body = await renderReply(reply, sent).response.json()
      console.log(JSON.stringify([body.id, ...body.content.map(block => block.id)]))
    `
    const child = Bun.spawn(['bun', '-e', script], { stdin: new TextEncoder().encode(JSON.stringify([TWO_TOOLS, sent])), stdout: 'pipe', stderr: 'pipe' })
    const [out, err, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
    expect(err).toBe('')
    expect(code).toBe(0)
    expect(JSON.parse(out)).toEqual(await ids(sent))
  })

  test('two requests get different ids, and two tool_use blocks in one reply get different ids', async () => {
    const first = await ids(request('one', false))
    const second = await ids(request('two', false))
    expect(new Set([...first, ...second]).size).toBe(6)
  })

  test('a stream and a JSON reply to one body carry the same ids', async () => {
    const streaming = request('either way', true)
    const { rendered } = renderReply(TWO_TOOLS, streaming)
    const json = renderReply(TWO_TOOLS, { ...streaming, stream: false }).rendered
    expect(rendered).toMatchObject({ kind: 'stream', id: (json as { id: string }).id })
  })
})

describe('a literal reply', () => {
  test('on a status with no body (204) carries none, so no Content-Length is sent', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'mock-anthropic-render-'))
    try {
      await Bun.write(join(dir, 'empty.json'), JSON.stringify({ reply: { kind: 'literal', status: 204, body: '' } }))
      const [script] = await readScripts([dir])
      const reply = script!.reply(request('nothing', false))!
      const { response } = renderReply(reply, request('nothing', false))
      expect(response.status).toBe(204)
      expect(response.body).toBeNull()
    } finally {
      await rm(dir, { recursive: true, force: true })
    }
  })

  test('is sent as written: a 429 keeps its status, headers and body, for a stream request or not', async () => {
    const body = JSON.stringify({ type: 'error', error: { type: 'rate_limit_error', message: 'Slow down' } })
    const reply: Reply = { kind: 'literal', status: 429, headers: { 'retry-after': '7', 'content-type': 'application/json', 'x-should-retry': 'true' }, body }
    for (const stream of [false, true]) {
      const { response, rendered } = renderReply(reply, request('too fast', stream))
      expect(response.status).toBe(429)
      expect(response.headers.get('retry-after')).toBe('7')
      expect(response.headers.get('content-type')).toBe('application/json')
      expect(response.headers.get('x-should-retry')).toBe('true')
      expect(await response.text()).toBe(body)
      expect(rendered).toEqual({ kind: 'literal', status: 429 })
    }
  })
})

describe('pacing', () => {
  test('a stream with no delay takes no time per event, however many deltas a long reply has', async () => {
    const text = 'x'.repeat(40_000)
    const started = performance.now()
    const stream = await events(renderReply(message([{ type: 'text', text }], 'end_turn'), request('long', true)).response)
    expect(stream.length).toBeGreaterThan(2500)
    // A timer per event would cost about 1.5 ms each: nearly four seconds here.
    expect(performance.now() - started).toBeLessThan(1000)
  })

  test("a stream waits the script's delay before each event after the first, and reports it", async () => {
    const delay = 40
    const started = performance.now()
    const { response, rendered } = renderReply(message([{ type: 'text', text: 'short' }], 'end_turn', delay), request('slowly', true))
    const stream = await events(response)
    const elapsed = performance.now() - started
    // message_start, the block's start, one delta, its stop, message_delta, message_stop: five waits.
    expect(stream).toHaveLength(6)
    expect(elapsed).toBeGreaterThanOrEqual((stream.length - 1) * delay - 5)
    expect(rendered).toMatchObject({ kind: 'stream', event_delay_ms: delay })
  })

  test('the first event is sent at once, and a client that leaves during a wait ends the stream', async () => {
    const { response } = renderReply(message([{ type: 'text', text: 'never sent' }], 'end_turn', 60_000), request('leave', true))
    const reader = response.body!.getReader()
    const started = performance.now()
    expect(new TextDecoder().decode((await reader.read()).value)).toStartWith('event: message_start\n')
    await reader.cancel('client left')
    expect(await reader.read()).toEqual({ done: true, value: undefined })
    expect(performance.now() - started).toBeLessThan(1000)
  })
})
