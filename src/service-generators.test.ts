import { describe, expect, test } from 'bun:test'
import { DEFAULT_ACCOUNT } from './account.ts'
import type { Method } from './endpoints.ts'
import { mockGenerators } from './generators.ts'
import { scriptDirectories, send as sendTo } from './testing.ts'

const { directory } = await scriptDirectories('service-generators')

const API = 'https://api.anthropic.com'
const send = (method: Method, url: string, body = '') => sendTo(mockGenerators({ account: DEFAULT_ACCOUNT, scenarioDirs: [] }), method, url, body)

// What Claude Code sends, as far as the answer depends on it.
const ANSWERS: readonly { name: string; method: Method; url: string; body?: string; status: number; type: string | null; answer: string }[] = [
  { name: 'feature-flag eval', method: 'POST', url: `${API}/api/eval/sdk-mock`, body: '{"attributes":{}}', status: 200, type: 'application/json', answer: '{"features":{}}' },
  { name: 'event logging batch', method: 'POST', url: `${API}/api/event_logging/batch`, body: '{"events":[]}', status: 200, type: 'application/json', answer: '{}' },
  { name: 'event logging v2 batch', method: 'POST', url: `${API}/api/event_logging/v2/batch`, body: '{"events":[]}', status: 200, type: 'application/json', answer: '{}' },
  { name: 'Datadog log intake', method: 'POST', url: 'https://http-intake.logs.us5.datadoghq.com/api/v2/logs', body: '[]', status: 200, type: 'application/json', answer: '{}' },
  { name: 'metrics_enabled', method: 'GET', url: `${API}/api/claude_code/organizations/metrics_enabled`, status: 200, type: 'application/json', answer: '{"metrics_logging_enabled":false}' },
  { name: 'remote managed settings, none', method: 'GET', url: `${API}/api/claude_code/settings`, status: 204, type: null, answer: '' },
  { name: 'policy limits', method: 'GET', url: `${API}/api/claude_code/policy_limits`, status: 200, type: 'application/json', answer: '{"restrictions":{}}' },
  { name: 'penguin mode', method: 'GET', url: `${API}/api/claude_code_penguin_mode`, status: 200, type: 'application/json', answer: '{"enabled":false,"disabled_reason":null}' },
  {
    name: 'grove config',
    method: 'GET',
    url: `${API}/api/claude_code_grove`,
    status: 200,
    type: 'application/json',
    answer: '{"grove_enabled":false,"domain_excluded":false,"notice_is_grace_period":true,"notice_reminder_frequency":null}',
  },
  { name: 'MCP registry', method: 'GET', url: `${API}/mcp-registry/v0/servers?version=latest&limit=100`, status: 200, type: 'application/json', answer: '{"servers":[],"metadata":{}}' },
  { name: 'MCP server list', method: 'GET', url: `${API}/v1/mcp_servers?limit=1000`, status: 200, type: 'application/json', answer: '{"data":[],"next_page":null}' },
  {
    name: 'marketplace version pointer',
    method: 'GET',
    url: 'https://downloads.claude.ai/claude-code-releases/plugins/claude-plugins-official/latest',
    status: 200,
    type: 'text/plain',
    answer: `${'0'.repeat(40)}\n`,
  },
  {
    name: 'changelog',
    method: 'GET',
    url: 'https://raw.githubusercontent.com/anthropics/claude-code/refs/heads/main/CHANGELOG.md',
    status: 200,
    type: 'text/plain',
    answer: '# Changelog\n',
  },
]

describe('the service generators', () => {
  for (const { name, method, url, body, status, type, answer } of ANSWERS) {
    test(`${name}: answers the same on every request, and reports nothing decided`, async () => {
      for (let i = 0; i < 2; i++) {
        const dispatched = await send(method, url, body)
        expect(dispatched.verdict).toEqual({ outcome: 'served' })
        expect(dispatched.response.status).toBe(status)
        expect(dispatched.response.headers.get('content-type')?.split(';')[0] ?? null).toBe(type)
        expect(await dispatched.response.text()).toBe(answer)
        expect(dispatched.facts).toEqual({})
      }
    })
  }
})

describe('token counting', () => {
  const COUNT = `${API}/v1/messages/count_tokens?beta=true`
  const request = (text: string) => JSON.stringify({ model: 'claude-sonnet-5-5', messages: [{ role: 'user', content: text }] })
  const count = async (body: string) => {
    const { verdict, response, facts } = await send('POST', COUNT, body)
    expect(verdict).toEqual({ outcome: 'served' })
    const answer = (await response.json()) as { input_tokens: number }
    expect(facts).toEqual({ input_tokens: answer.input_tokens })
    return answer.input_tokens
  }

  test('the same request counts the same, and a longer one counts more', async () => {
    const short = await count(request('hello'))
    expect(short).toBeGreaterThan(0)
    expect(await count(request('hello'))).toBe(short)
    expect(await count(request('hello '.repeat(100)))).toBeGreaterThan(short)
  })

  // [LAW:one-source-of-truth] A conversation counts the same as a token count request and as a Messages
  // request, which also carries the fields that shape only the reply.
  test('a conversation counts what a Messages reply to it reports as its input', async () => {
    const body = request('hello')
    const script = { reply: { kind: 'message', content: [{ type: 'text', text: 'hi' }], stop_reason: 'end_turn' } }
    const dir = await directory({ 'a.json': script })
    const messagesBody = JSON.stringify({ ...JSON.parse(body), max_tokens: 32000, stream: false, metadata: { user_id: 'someone' } })
    const reply = await sendTo(mockGenerators({ account: DEFAULT_ACCOUNT, scenarioDirs: [dir] }), 'POST', `${API}/v1/messages`, messagesBody)
    const usage = ((await reply.response.json()) as { usage: { input_tokens: number } }).usage
    expect(usage.input_tokens).toBe(await count(body))
  })

  for (const [name, body, why] of [
    ['a body that is not JSON', 'model=x', /^body is not JSON/],
    ['a body with no messages', '{"model":"claude-sonnet-5-5","messages":[]}', /^body is not a token count request/],
    ['a body whose messages are not messages', '{"model":"claude-sonnet-5-5","messages":[1]}', /^body is not a token count request/],
  ] as const) {
    test(`${name} is declined, saying why`, async () => {
      const { verdict, response, facts } = await send('POST', COUNT, body)
      expect(verdict).toMatchObject({ outcome: 'unmatched', why: expect.stringMatching(why) })
      expect(response.status).toBe(400)
      expect(facts).toEqual({})
    })
  }
})
