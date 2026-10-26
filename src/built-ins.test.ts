import { describe, expect, test } from 'bun:test'
import { join } from 'node:path'
import { BUILT_INS } from './built-ins.ts'
import { scriptDirectories, sendMessages } from './testing.ts'

const { directory } = await scriptDirectories('built-ins')

// A Messages request with the built-ins after the given consumer directories, as the server answers it.
const send = (body: unknown, dirs: readonly string[] = []) => sendMessages(JSON.stringify(body), dirs, BUILT_INS)

// The text of a JSON reply. How a reply streams is the renderer's, the same for every reply.
async function replyText(response: Response): Promise<string> {
  const message = (await response.json()) as { content: { text: string }[] }
  return message.content.map(block => block.text).join('')
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
const titleRequest = (opening: string) =>
  sideQuery(`${opening} The title is a name for what the session is about.\n\nReturn JSON with a single "title" field.`, '<session>\nmake the parser faster\n</session>\n\nWrite the title in English.', {
    output_config: { format: { type: 'json_schema', schema: TITLE_SCHEMA } },
  })
const TITLE_CURRENT = 'You are naming a coding session so the user can pick it out of a long list of sessions.'
const TITLE_OLDER = 'Generate a concise, sentence-case title'

const CLASSIFIER = 'You are a security monitor for autonomous AI coding agents.'
const SEVERITY_FORMAT = '## Output Format\n\nOutput <severity>N</severity> where N is an integer 0-100 and 50 is exactly the allow/block boundary.'
const BLOCK_FORMAT = '## Output Format\n\nIf the action should be blocked:\n<block>yes</block>\n\nIf the action should be allowed:\n<block>no</block>'
const TOOL_FORMAT = 'Use the classify_result tool to report your classification.'
const OTHER_FORMAT = '## Output Format\n\nAnswer ALLOW or DENY.'
// Each format's prompt is sent with its own closing instruction, and the tool format with the tool.
const classifierRequest = (format: string, closing: string, extra: Record<string, unknown> = {}) =>
  sideQuery(
    `${CLASSIFIER}\n\n## Context\n\nThe agent you are monitoring is an autonomous coding agent.\n\n${format}`,
    [
      { type: 'text', text: '<transcript>\n' },
      { type: 'text', text: 'Bash: ls -la\n' },
      { type: 'text', text: '</transcript>\n' },
      { type: 'text', text: closing },
    ],
    { max_tokens: 64, ...extra },
  )
const severityRequest = () => classifierRequest(SEVERITY_FORMAT, 'Respond with <severity>N</severity> ONLY.', { stop_sequences: ['</severity>'] })
const blockRequest = () => classifierRequest(BLOCK_FORMAT, '\nErr on the side of blocking. <block> immediately.')
const toolRequest = () =>
  classifierRequest(TOOL_FORMAT, 'Classify the action.', {
    tools: [{ name: 'classify_result', input_schema: { type: 'object' } }],
    tool_choice: { type: 'tool', name: 'classify_result' },
  })

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
    ['2.1.283 on', TITLE_CURRENT],
    ['older', TITLE_OLDER],
  ] as const) {
    test(`the session title (${phrasing} phrasing) is the JSON object its output format asks for`, async () => {
      const { verdict, response, facts } = await send(titleRequest(opening))
      expect(verdict).toEqual({ outcome: 'served' })
      const answer = JSON.parse(await replyText(response)) as unknown
      expect(answer).toEqual({ title: expect.stringMatching(/\S/) })
      expect(Object.keys(answer as object)).toEqual(['title'])
      expect(facts).toMatchObject({ script: 'built-in: session title' })
    })
  }

  test('the auto mode classifier, asked for a severity, allows', async () => {
    const { verdict, response, facts } = await send(severityRequest())
    expect(verdict).toEqual({ outcome: 'served' })
    expect(severity(await replyText(response))).toBeLessThan(50)
    expect(facts).toMatchObject({ script: 'built-in: auto mode classifier, severity', reply: { stop_reason: 'end_turn' } })
  })

  test('the quota probe gets a one-token message', async () => {
    const { verdict, response, facts } = await send(QUOTA_PROBE)
    expect(verdict).toEqual({ outcome: 'served' })
    expect(response.status).toBe(200)
    expect(((await response.json()) as { stop_reason: string }).stop_reason).toBe('max_tokens')
    expect(facts).toMatchObject({ script: 'built-in: quota probe', reply: { kind: 'json', stop_reason: 'max_tokens' } })
  })

  test('the auto mode classifier, asked for a block decision, allows', async () => {
    const { verdict, response, facts } = await send(blockRequest())
    expect(verdict).toEqual({ outcome: 'served' })
    // Claude Code's reading of a block answer: the first <block> tag decides, and `no` allows.
    expect([...(await replyText(response)).matchAll(/<block>(yes|no)\b/gi)].map(tag => tag[1])).toEqual(['no'])
    expect(facts).toMatchObject({ script: 'built-in: auto mode classifier, block', reply: { stop_reason: 'end_turn' } })
  })

  test('the auto mode classifier, asked for a classify_result call, allows', async () => {
    const { verdict, response, facts } = await send(toolRequest())
    expect(verdict).toEqual({ outcome: 'served' })
    // Claude Code's reading of a tool answer: the classify_result call's input, which must parse.
    const message = (await response.json()) as { content: { type: string; id?: string; name?: string; input?: unknown }[] }
    expect(message.content.filter(block => block.type === 'tool_use')).toEqual([
      { type: 'tool_use', id: expect.any(String), name: 'classify_result', input: { thinking: expect.any(String), shouldBlock: false, reason: expect.any(String) } },
    ])
    expect(facts).toMatchObject({ script: 'built-in: auto mode classifier, classify_result tool', reply: { stop_reason: 'tool_use' } })
  })

  test('a consumer script blocks the classifier, ahead of the built-in', async () => {
    const dir = await directory({ 'block.json': { match: { bodyContains: [CLASSIFIER] }, reply: says('<severity>72</severity>') } })
    const { response, facts } = await send(severityRequest(), [dir])
    expect(severity(await replyText(response))).toBeGreaterThanOrEqual(50)
    expect(facts).toMatchObject({ script: join(dir, 'block.json') })
  })

  test('a consumer script overrides the session title, ahead of the built-in', async () => {
    const dir = await directory({ 'title.json': { match: { bodyContains: [TITLE_CURRENT] }, reply: says('{"title":"Parser speed"}') } })
    const { response, facts } = await send(titleRequest(TITLE_CURRENT), [dir])
    expect(JSON.parse(await replyText(response))).toEqual({ title: 'Parser speed' })
    expect(facts).toMatchObject({ script: join(dir, 'title.json') })
  })

  test('an unrecognized prompt with no consumer script gets the 400, having tried every built-in', async () => {
    const { verdict, response, facts } = await send({ model: MODEL, max_tokens: 1024, messages: [{ role: 'user', content: 'hello' }] })
    expect(verdict).toEqual({ outcome: 'unmatched', why: `no script matches: ${BUILT_INS.length} tried` })
    expect(response.status).toBe(400)
    expect(facts).toEqual({ scripts_tried: BUILT_INS.map(script => script.origin) })
  })

  test('a classifier asking for an answer in a format no built-in knows gets the 400', async () => {
    const { verdict, response } = await send(classifierRequest(OTHER_FORMAT, 'Answer now.'))
    expect(verdict).toMatchObject({ outcome: 'unmatched' })
    expect(response.status).toBe(400)
  })

  test("a main turn that quotes the prompts is not one of Claude Code's own calls", async () => {
    // Quoted in the conversation, and in the main prompt's block after Claude Code's own opening, as an
    // appended system prompt would be.
    const quoted = `What does "${TITLE_CURRENT}" mean, and "${CLASSIFIER} ${SEVERITY_FORMAT}"? Also: quota`
    const { verdict } = await send({
      model: MODEL,
      max_tokens: 1,
      system: [BILLING, { type: 'text', text: `You are Claude Code.\n\n${TITLE_OLDER} ${quoted}` }],
      messages: [{ role: 'user', content: quoted }],
    })
    expect(verdict).toMatchObject({ outcome: 'unmatched' })
  })
})
