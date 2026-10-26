import { expect, test } from 'bun:test'
import { join } from 'node:path'
import { DEFAULT_ACCOUNT } from './account.ts'
import { mockGenerators } from './generators.ts'
import { scriptDirectories, send } from './testing.ts'

const { directory } = await scriptDirectories('generators')
const MESSAGES = 'https://api.anthropic.com/v1/messages'
const body = (text: string, max_tokens = 1024) => JSON.stringify({ model: 'claude-sonnet-5-5', max_tokens, messages: [{ role: 'user', content: text }] })

const says = (text: string) => ({ reply: { kind: 'message', content: [{ type: 'text', text }], stop_reason: 'end_turn' } })

test("Messages are answered from the consumer's script directories, in the order given", async () => {
  // Given against the order their paths sort in, so only the given order answers from `second`.
  const [first, second] = [await directory({ 'a.json': says('first') }), await directory({ 'a.json': says('second') })]
  const { facts } = await send(mockGenerators({ account: DEFAULT_ACCOUNT, scenarioDirs: [second, first] }), 'POST', MESSAGES, body('hello'))
  expect(facts).toMatchObject({ script: join(second, 'a.json') })
})

test("Claude Code's own model calls are answered by the built-ins when no consumer script matches", async () => {
  const unmatched = await directory({ 'a.json': { match: { bodyContains: ['never sent'] }, ...says('consumer') } })
  const { facts } = await send(mockGenerators({ account: DEFAULT_ACCOUNT, scenarioDirs: [unmatched] }), 'POST', MESSAGES, body('quota', 1))
  expect(facts).toMatchObject({ script: 'built-in: quota probe' })
})
