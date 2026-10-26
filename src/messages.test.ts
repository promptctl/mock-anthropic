import { describe, expect, test } from 'bun:test'
import { type MessagesRequest, parseMessagesRequest } from './messages.ts'

const MODEL = 'claude-sonnet-5-5'
// The fields the API requires beside the messages.
const REQUIRED = { model: MODEL, max_tokens: 1024 }

function parsed(body: unknown): MessagesRequest {
  const result = parseMessagesRequest(JSON.stringify(body))
  if (result.kind !== 'parsed') throw new Error(`expected a Messages request: ${result.why}`)
  return result.value
}

function invalid(raw: string): string {
  const result = parseMessagesRequest(raw)
  if (result.kind !== 'invalid') throw new Error(`expected an invalid body: ${raw}`)
  return result.why
}

describe('parseMessagesRequest', () => {
  test('keeps the raw body and passes the fields Claude Code sends that nothing reads', () => {
    const body = {
      model: MODEL,
      max_tokens: 1024,
      stream: true,
      system: [{ type: 'text', text: 'You are Claude Code.' }],
      tools: [{ name: 'Bash', input_schema: { type: 'object' } }],
      metadata: { user_id: 'u' },
      messages: [{ role: 'user', content: 'hello', cache_control: { type: 'ephemeral' } }],
    }
    const request = parsed(body)
    expect(request.raw).toBe(JSON.stringify(body))
    expect(request.lastUserTexts).toEqual(['hello'])
    expect(request.model).toBe(MODEL)
    expect(request.stream).toBe(true)
  })

  test('a request that does not ask for a stream is not one, as the API defaults it', () => {
    expect(parsed({ ...REQUIRED, messages: [{ role: 'user', content: 'hi' }] }).stream).toBe(false)
    expect(parsed({ ...REQUIRED, stream: false, messages: [{ role: 'user', content: 'hi' }] }).stream).toBe(false)
  })

  test('the last user text is the last user message, not an earlier one or a later assistant turn', () => {
    const request = parsed({
      ...REQUIRED,
      messages: [
        { role: 'user', content: 'first question' },
        { role: 'assistant', content: [{ type: 'text', text: 'first answer' }] },
        { role: 'user', content: 'second question' },
        { role: 'assistant', content: 'prefill' },
      ],
    })
    expect(request.lastUserTexts).toEqual(['second question'])
  })

  test('the texts of a message are its text blocks and its tool results, by block type', () => {
    const request = parsed({
      ...REQUIRED,
      messages: [
        {
          role: 'user',
          content: [
            { type: 'tool_result', tool_use_id: 'toolu_secret_id', content: 'string result' },
            { type: 'tool_result', tool_use_id: 'toolu_other', content: [{ type: 'text', text: 'block result' }, { type: 'image', source: { data: 'aW1hZ2U=' } }] },
            { type: 'tool_result', tool_use_id: 'toolu_empty' },
            { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'aW1hZ2U=' } },
            { type: 'text', text: 'typed text' },
          ],
        },
      ],
    })
    expect(request.lastUserTexts).toEqual(['string result', 'block result', 'typed text'])
  })

  test('a system-role message is read as part of the conversation, neither the current turn nor the system prompt', () => {
    // The shape 2.1.289 sends when a SessionStart hook adds context.
    const request = parsed({
      ...REQUIRED,
      system: [{ type: 'text', text: 'You are Claude Code.' }],
      messages: [
        { role: 'user', content: [{ type: 'text', text: 'Say hello and nothing else.' }] },
        { role: 'system', content: [{ type: 'text', text: 'SessionStart hook additional context: be brief' }] },
      ],
    })
    expect(request.lastUserTexts).toEqual(['Say hello and nothing else.'])
    expect(request.systemTexts).toEqual(['You are Claude Code.'])
    invalid(JSON.stringify({ ...REQUIRED, messages: [{ role: 'user', content: 'hi' }, { role: 'system', content: [{ type: 'text', text: 5 }] }] }))
  })

  test('a body that is not a Messages request is invalid, with the reason', () => {
    expect(invalid('{not json')).toStartWith('body is not JSON: ')
    expect(invalid('[]')).toStartWith('body is not a Messages request: ')
    expect(invalid(JSON.stringify({ ...REQUIRED }))).toContain('messages')
    expect(invalid(JSON.stringify({ ...REQUIRED, messages: [] }))).toContain('messages')
    expect(invalid(JSON.stringify({ ...REQUIRED, messages: [{ role: 'assistant', content: 'only me' }] }))).toContain('has no user message')
    expect(invalid(JSON.stringify({ ...REQUIRED, messages: [{ role: 'system', content: 'x' }] }))).toContain('has no user message')
    expect(invalid(JSON.stringify({ ...REQUIRED, messages: [{ role: 'tool', content: 'x' }] }))).toContain('role')
    expect(invalid(JSON.stringify({ max_tokens: 1024, messages: [{ role: 'user', content: 'x' }] }))).toContain('model')
    expect(invalid(JSON.stringify({ ...REQUIRED, model: '', messages: [{ role: 'user', content: 'x' }] }))).toContain('model')
    expect(invalid(JSON.stringify({ ...REQUIRED, stream: 'yes', messages: [{ role: 'user', content: 'x' }] }))).toContain('stream')
  })

  test('the system texts are the system prompt, one per block, and none when there is no system prompt', () => {
    const hi = [{ role: 'user', content: 'hi' }]
    expect(parsed({ ...REQUIRED, system: 'You are terse.', messages: hi }).systemTexts).toEqual(['You are terse.'])
    const blocks = [{ type: 'text', text: 'billing header' }, { type: 'text', text: 'You are terse.', cache_control: { type: 'ephemeral' } }]
    expect(parsed({ ...REQUIRED, system: blocks, messages: hi }).systemTexts).toEqual(['billing header', 'You are terse.'])
    expect(parsed({ ...REQUIRED, messages: hi }).systemTexts).toEqual([])
  })

  test('max_tokens is required and positive, as the API has it', () => {
    const hi = [{ role: 'user', content: 'hi' }]
    expect(parsed({ ...REQUIRED, max_tokens: 1, messages: hi }).max_tokens).toBe(1)
    expect(invalid(JSON.stringify({ model: MODEL, messages: hi }))).toContain('max_tokens')
    expect(invalid(JSON.stringify({ ...REQUIRED, max_tokens: 0, messages: hi }))).toContain('max_tokens')
    expect(invalid(JSON.stringify({ ...REQUIRED, max_tokens: 1.5, messages: hi }))).toContain('max_tokens')
  })

  test('a system prompt holding anything but text blocks is invalid', () => {
    const hi = [{ role: 'user', content: 'hi' }]
    expect(invalid(JSON.stringify({ ...REQUIRED, system: [{ type: 'image', source: {} }], messages: hi }))).toContain('system')
    expect(invalid(JSON.stringify({ ...REQUIRED, system: 5, messages: hi }))).toContain('system')
  })

  test('a malformed block of a type it reads does not pass as some other block', () => {
    invalid(JSON.stringify({ ...REQUIRED, messages: [{ role: 'user', content: [{ type: 'text', text: 5 }] }] }))
    invalid(JSON.stringify({ ...REQUIRED, messages: [{ role: 'user', content: [{ type: 'tool_result', content: 5 }] }] }))
    invalid(JSON.stringify({ ...REQUIRED, messages: [{ role: 'user', content: [{ type: 'tool_result', content: [{ type: 'text' }] }] }] }))
  })
})
