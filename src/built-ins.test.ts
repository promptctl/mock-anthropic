import { afterAll, describe, expect, test } from 'bun:test'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { BUILT_INS } from './built-ins.ts'
import { dispatch, type Generators, type MockRequest } from './dispatch.ts'
import { API, ENDPOINT_TABLE, resolveEndpoint, type Row } from './endpoints.ts'
import { messagesGenerator } from './messages-generator.ts'

const root = await mkdtemp(join(tmpdir(), 'mock-anthropic-built-ins-'))
afterAll(() => rm(root, { recursive: true, force: true }))

let nextDir = 0
async function directory(scripts: Record<string, unknown> = {}): Promise<string> {
  const dir = join(root, String(nextDir++))
  await mkdir(dir)
  for (const [name, script] of Object.entries(scripts)) await Bun.write(join(dir, name), JSON.stringify(script))
  return dir
}

// A Messages request through dispatch, with the built-ins after the given consumer directories, as
// the server answers it.
function send(body: unknown, dirs: readonly string[] = []) {
  const request: MockRequest = { method: 'POST', url: new URL(`https://${API}/v1/messages`), headers: new Headers(), body: JSON.stringify(body) }
  const generators: Generators<Row> = { messages: messagesGenerator(dirs, BUILT_INS) }
  return dispatch(generators, resolveEndpoint(ENDPOINT_TABLE, request.method, request.url), request)
}

// The text a reply carries, whether streamed or sent as one JSON message.
async function replyText(response: Response): Promise<string> {
  if (!response.headers.get('content-type')?.startsWith('text/event-stream')) {
    const message = (await response.json()) as { content: { text: string }[] }
    return message.content.map(block => block.text).join('')
  }
  return (await response.text())
    .split('\n')
    .filter(line => line.startsWith('data: '))
    .map(line => JSON.parse(line.slice('data: '.length)) as { type: string; delta?: { type: string; text?: string } })
    .flatMap(event => (event.type === 'content_block_delta' && event.delta?.type === 'text_delta' ? [event.delta.text] : []))
    .join('')
}

// Bodies in the shapes Claude Code sends, written here: its side queries put their prompt in its own
// system block after a billing-header block.
const MODEL = 'claude-haiku-5-5'
const BILLING = { type: 'text', text: 'x-anthropic-billing-header: cc_version=0.0.0;' }
const sideQuery = (prompt: string, user: unknown, extra: Record<string, unknown> = {}) => ({
  model: MODEL,
  max_tokens: 32000,
  system: [BILLING, { type: 'text', text: prompt }],
  messages: [{ role: 'user', content: user }],
  ...extra,
})

const TITLE_SCHEMA = { type: 'object', properties: { title: { type: 'string' } }, required: ['title'], additionalProperties: false }
const titleRequest = (opening: string, stream: boolean) =>
  sideQuery(`${opening} The title is a name for what the session is about.\n\nReturn JSON with a single "title" field.`, '<session>\nmake the parser faster\n</session>\n\nWrite the title in English.', {
    stream,
    output_config: { format: { type: 'json_schema', schema: TITLE_SCHEMA } },
  })
const TITLE_2_1_289 = 'You are naming a coding session so the user can pick it out of a long list of sessions.'
const TITLE_OLDER = 'Generate a concise, sentence-case title'

const CLASSIFIER = 'You are a security monitor for autonomous AI coding agents.'
const SEVERITY_FORMAT = '## Output Format\n\nOutput <severity>N</severity> where N is an integer 0-100 and 50 is exactly the allow/block boundary.'
const BLOCK_FORMAT = '## Output Format\n\nIf the action should be blocked:\n<block>yes</block>\n\nIf the action should be allowed:\n<block>no</block>'
const classifierRequest = (format: string) =>
  sideQuery(
    `${CLASSIFIER}\n\n## Context\n\nThe agent you are monitoring is an autonomous coding agent.\n\n${format}`,
    [
      { type: 'text', text: '<transcript>\n' },
      { type: 'text', text: 'Bash: ls -la\n' },
      { type: 'text', text: '</transcript>\n' },
      { type: 'text', text: 'Respond with <severity>N</severity> ONLY.' },
    ],
    { max_tokens: 64, stop_sequences: ['</severity>'] },
  )

const QUOTA_PROBE = { model: MODEL, max_tokens: 1, messages: [{ role: 'user', content: 'quota' }] }

// Claude Code's reading of a severity answer: exactly one tag, its number the severity.
function severity(text: string): number {
  const tags = [...text.matchAll(/<severity>\s*(\d+(?:\.\d+)?)\s*(<\/severity>)?/g)]
  expect(tags).toHaveLength(1)
  return Number(tags[0]?.[1])
}

const says = (text: string) => ({ kind: 'message', content: [{ type: 'text', text }], stop_reason: 'end_turn' })

describe('built-in answers', () => {
  for (const [phrasing, opening] of [
    ['2.1.289', TITLE_2_1_289],
    ['older', TITLE_OLDER],
  ] as const) {
    for (const stream of [false, true]) {
      test(`the session title (${phrasing} phrasing, ${stream ? 'streamed' : 'JSON'}) is the JSON object its output format asks for`, async () => {
        const { verdict, response, facts } = await send(titleRequest(opening, stream))
        expect(verdict).toEqual({ outcome: 'served' })
        const answer = JSON.parse(await replyText(response)) as unknown
        expect(answer).toEqual({ title: expect.stringMatching(/\S/) })
        expect(Object.keys(answer as object)).toEqual(['title'])
        expect(facts).toMatchObject({ script: 'built-in: session title' })
      })
    }
  }

  test('the auto mode classifier, asked for a severity, allows', async () => {
    const { verdict, response, facts } = await send(classifierRequest(SEVERITY_FORMAT))
    expect(verdict).toEqual({ outcome: 'served' })
    expect(severity(await replyText(response))).toBeLessThan(50)
    expect(facts).toMatchObject({ script: 'built-in: auto mode classifier', reply: { stop_reason: 'end_turn' } })
  })

  test('the quota probe gets a one-token message', async () => {
    const { verdict, response, facts } = await send(QUOTA_PROBE)
    expect(verdict).toEqual({ outcome: 'served' })
    expect(response.status).toBe(200)
    expect(((await response.json()) as { stop_reason: string }).stop_reason).toBe('max_tokens')
    expect(facts).toMatchObject({ script: 'built-in: quota probe', reply: { kind: 'json', stop_reason: 'max_tokens' } })
  })

  test('a consumer script blocks the classifier, ahead of the built-in', async () => {
    const dir = await directory({ 'block.json': { match: { bodyContains: [CLASSIFIER] }, reply: says('<severity>72</severity>') } })
    const { response, facts } = await send(classifierRequest(SEVERITY_FORMAT), [dir])
    expect(severity(await replyText(response))).toBeGreaterThanOrEqual(50)
    expect(facts).toMatchObject({ script: join(dir, 'block.json') })
  })

  test('a consumer script overrides the session title, ahead of the built-in', async () => {
    const dir = await directory({ 'title.json': { match: { bodyContains: [TITLE_2_1_289] }, reply: says('{"title":"Parser speed"}') } })
    const { response, facts } = await send(titleRequest(TITLE_2_1_289, false), [dir])
    expect(JSON.parse(await replyText(response))).toEqual({ title: 'Parser speed' })
    expect(facts).toMatchObject({ script: join(dir, 'title.json') })
  })

  test('an unrecognized prompt with no consumer script gets the 400, having tried every built-in', async () => {
    const { verdict, response, facts } = await send({ model: MODEL, max_tokens: 1024, messages: [{ role: 'user', content: 'hello' }] })
    expect(verdict).toEqual({ outcome: 'unmatched', why: `no script matches: ${BUILT_INS.length} tried` })
    expect(response.status).toBe(400)
    expect(facts).toEqual({ scripts_tried: BUILT_INS.map(script => script.origin) })
  })

  test('a classifier asking for a block decision, which no built-in answers, gets the 400', async () => {
    const { verdict, response } = await send(classifierRequest(BLOCK_FORMAT))
    expect(verdict).toMatchObject({ outcome: 'unmatched' })
    expect(response.status).toBe(400)
  })

  test("a main turn that quotes the prompts is not one of Claude Code's own calls", async () => {
    const quoted = `What does "${TITLE_2_1_289}" mean, and "${CLASSIFIER} ${SEVERITY_FORMAT}"? Also: quota`
    const { verdict } = await send({
      model: MODEL,
      max_tokens: 1,
      system: [BILLING, { type: 'text', text: 'You are Claude Code.' }],
      messages: [{ role: 'user', content: quoted }],
    })
    expect(verdict).toMatchObject({ outcome: 'unmatched' })
  })
})
