import { describe, expect, test } from 'bun:test'
import { join } from 'node:path'
import type { Script } from './scripts.ts'
import { scriptDirectories, sendMessages } from './testing.ts'

const { directory } = await scriptDirectories('messages')

const MODEL = 'claude-sonnet-5-5'
const body = (text: string, stream: boolean) => JSON.stringify({ model: MODEL, max_tokens: 1024, stream, messages: [{ role: 'user', content: text }] })

function send(dirs: readonly string[], text: string, { stream = false, builtIns = [] as readonly Script[], raw = body(text, stream) } = {}) {
  return sendMessages(raw, dirs, builtIns)
}

const SAYS_HELLO = { match: { lastUserContains: ['hello'] }, reply: { kind: 'message', content: [{ type: 'text', text: 'Hello!' }], stop_reason: 'end_turn' } }
const RATE_LIMITED = {
  reply: { kind: 'literal', status: 429, headers: { 'retry-after': '3', 'content-type': 'application/json' }, body: '{"type":"error","error":{"type":"rate_limit_error","message":"Slow down"}}' },
}

describe('the Messages generator', () => {
  test('answers with the first matching script as JSON, and its facts name the script and the message sent', async () => {
    const dir = await directory({ 'hello.json': SAYS_HELLO })
    const { verdict, response, facts } = await send([dir], 'hello there')
    expect(verdict).toEqual({ outcome: 'served' })
    const message = (await response.json()) as { id: string; content: unknown }
    expect(message.content).toEqual([{ type: 'text', text: 'Hello!' }])
    expect(facts).toEqual({ script: join(dir, 'hello.json'), reply: { kind: 'json', id: message.id, stop_reason: 'end_turn' } })
  })

  test('streams when the request asks, and its facts say so, with the delay in force', async () => {
    const dir = await directory({ 'hello.json': { ...SAYS_HELLO, reply: { ...SAYS_HELLO.reply, eventDelayMs: 1 } } })
    const { response, facts } = await send([dir], 'hello', { stream: true })
    const text = await response.text()
    expect(text).toStartWith('event: message_start\n')
    expect(facts).toEqual({ script: join(dir, 'hello.json'), reply: { kind: 'stream', id: expect.stringMatching(/^msg_/), stop_reason: 'end_turn', event_delay_ms: 1 } })
    // The recorded id is the one the stream carried, so the record joins the consumer's transcript.
    expect(text).toContain(`"id":"${(facts.reply as { id: string }).id}"`)
  })

  test('returns a scripted 429 verbatim', async () => {
    const dir = await directory({ 'limit.json': RATE_LIMITED })
    const { verdict, response, facts } = await send([dir], 'anything', { stream: true })
    expect(verdict).toEqual({ outcome: 'served' })
    expect(response.status).toBe(429)
    expect(response.headers.get('retry-after')).toBe('3')
    expect(response.headers.get('content-type')).toBe('application/json')
    expect(await response.text()).toBe(RATE_LIMITED.reply.body)
    expect(facts).toEqual({ script: join(dir, 'limit.json'), reply: { kind: 'literal', status: 429 } })
  })

  test("a built-in answers when no consumer script does, and the facts carry the built-in's name", async () => {
    const builtIn: Script = { origin: 'built-in: test', reply: () => ({ kind: 'literal', status: 200, headers: {}, body: 'built-in' }) }
    const dir = await directory({ 'hello.json': SAYS_HELLO })
    const { response, facts } = await send([dir], 'goodbye', { builtIns: [builtIn] })
    expect(await response.text()).toBe('built-in')
    expect(facts).toMatchObject({ script: 'built-in: test' })
  })

  test('declines a request no script matches, naming how many were tried, and the facts list them', async () => {
    const dir = await directory({ 'hello.json': SAYS_HELLO })
    const { verdict, response, facts } = await send([dir], 'goodbye')
    expect(verdict).toEqual({ outcome: 'unmatched', why: 'no script matches: 1 tried' })
    expect(response.status).toBe(400)
    expect(facts).toEqual({ scripts_tried: [join(dir, 'hello.json')] })
  })

  test('a request that found no scripts at all says 0 tried', async () => {
    const { verdict, facts } = await send([await directory()], 'hello')
    expect(verdict).toEqual({ outcome: 'unmatched', why: 'no script matches: 0 tried' })
    expect(facts).toEqual({ scripts_tried: [] })
  })

  test('declines a body that is not a Messages request with the reason, having matched no script', async () => {
    const dir = await directory({ 'any.json': RATE_LIMITED })
    const { verdict, facts } = await send([dir], '', { raw: '{"messages": "nope"' })
    expect(verdict).toMatchObject({ outcome: 'unmatched', why: expect.stringMatching(/^body is not JSON: /) })
    expect(facts).toEqual({})
  })

  test('a bad script fails the request with a 500 that names its path, even when an earlier script matches', async () => {
    const dir = await directory({ 'a.json': SAYS_HELLO, 'b.json': '{ not json' })
    const { verdict, response, facts } = await send([dir], 'hello')
    expect(verdict).toMatchObject({ outcome: 'failed', why: expect.stringContaining(join(dir, 'b.json')) })
    expect(response.status).toBe(500)
    expect(facts).toEqual({})
  })

  test('a bad script fails a request whose body is not a Messages request too', async () => {
    const dir = await directory({ 'b.json': '{ not json' })
    const { verdict, response } = await send([dir], '', { raw: '{"messages": "nope"' })
    expect(verdict).toMatchObject({ outcome: 'failed', why: expect.stringContaining(join(dir, 'b.json')) })
    expect(response.status).toBe(500)
  })
})
