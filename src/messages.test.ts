import { describe, expect, test } from 'bun:test'
import { type MessagesRequest, parseMessagesRequest } from './messages.ts'

function parsed(body: unknown): MessagesRequest {
  const result = parseMessagesRequest(JSON.stringify(body))
  if (result.kind !== 'parsed') throw new Error(`expected a Messages request: ${result.why}`)
  return result.request
}

function invalid(raw: string): string {
  const result = parseMessagesRequest(raw)
  if (result.kind !== 'invalid') throw new Error(`expected an invalid body: ${raw}`)
  return result.why
}

describe('parseMessagesRequest', () => {
  test('keeps the raw body and passes the fields Claude Code sends that nothing reads', () => {
    const body = {
      model: 'claude-sonnet-5-5',
      max_tokens: 1024,
      stream: true,
      system: [{ type: 'text', text: 'You are Claude Code.' }],
      tools: [{ name: 'Bash', input_schema: { type: 'object' } }],
      metadata: { user_id: 'u' },
      messages: [{ role: 'user', content: 'hello', cache_control: { type: 'ephemeral' } }],
    }
    const request = parsed(body)
    expect(request.raw).toBe(JSON.stringify(body))
    expect(request.lastUserText).toBe('hello')
  })

  test('the last user text is the last user message, not an earlier one or a later assistant turn', () => {
    const request = parsed({
      messages: [
        { role: 'user', content: 'first question' },
        { role: 'assistant', content: [{ type: 'text', text: 'first answer' }] },
        { role: 'user', content: 'second question' },
        { role: 'assistant', content: 'prefill' },
      ],
    })
    expect(request.lastUserText).toBe('second question')
  })

  test('the text of a message is its text blocks and its tool results, by block type', () => {
    const request = parsed({
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
    expect(request.lastUserText).toBe('string result\nblock result\ntyped text')
  })

  test('a body that is not a Messages request is invalid, with the reason', () => {
    expect(invalid('{not json')).toStartWith('body is not JSON: ')
    expect(invalid('[]')).toStartWith('body is not a Messages request: ')
    expect(invalid(JSON.stringify({ model: 'm' }))).toContain('messages')
    expect(invalid(JSON.stringify({ messages: [] }))).toContain('messages')
    expect(invalid(JSON.stringify({ messages: [{ role: 'assistant', content: 'only me' }] }))).toContain('has no user message')
    expect(invalid(JSON.stringify({ messages: [{ role: 'system', content: 'x' }] }))).toContain('role')
  })

  test('a malformed block of a type it reads does not pass as some other block', () => {
    invalid(JSON.stringify({ messages: [{ role: 'user', content: [{ type: 'text', text: 5 }] }] }))
    invalid(JSON.stringify({ messages: [{ role: 'user', content: [{ type: 'tool_result', content: 5 }] }] }))
    invalid(JSON.stringify({ messages: [{ role: 'user', content: [{ type: 'tool_result', content: [{ type: 'text' }] }] }] }))
  })
})
