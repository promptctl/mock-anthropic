import { afterAll, describe, expect, test } from 'bun:test'
import { mkdir, mkdtemp, rm, symlink } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { type MessagesRequest, parseMessagesRequest } from './messages.ts'
import { type Reply, type Script, type Selection, readScripts, selectReply } from './scripts.ts'

const root = await mkdtemp(join(tmpdir(), 'mock-anthropic-scripts-'))
afterAll(() => rm(root, { recursive: true, force: true }))

let nextDir = 0
async function directory(scripts: Record<string, unknown> = {}): Promise<string> {
  const dir = join(root, String(nextDir++))
  await mkdir(dir)
  for (const [name, script] of Object.entries(scripts)) await writeScript(dir, name, script)
  return dir
}

async function writeScript(dir: string, name: string, script: unknown): Promise<string> {
  const path = join(dir, name)
  await Bun.write(path, typeof script === 'string' ? script : JSON.stringify(script))
  return path
}

function request(...messages: { role: 'user' | 'assistant'; content: unknown }[]): MessagesRequest {
  const result = parseMessagesRequest(JSON.stringify({ model: 'claude-sonnet-5-5', max_tokens: 1024, messages }))
  if (result.kind !== 'parsed') throw new Error(result.why)
  return result.request
}

const HELLO = request({ role: 'user', content: 'hello' })

function says(text: string, match: Record<string, string[]> = {}) {
  return { match, reply: { kind: 'message', content: [{ type: 'text', text }], stop_reason: 'end_turn' } }
}

function matched(selection: Selection): { origin: string; reply: Reply } {
  if (selection.kind !== 'matched') throw new Error(`expected a match; tried ${selection.tried.join(', ')}`)
  return selection
}

function textOf(selection: Selection): string {
  const { reply } = matched(selection)
  if (reply.kind !== 'message' || reply.content[0]?.type !== 'text') throw new Error('expected a text reply')
  return reply.content[0].text
}

const EVERYTHING: Script = {
  origin: 'built-in: everything',
  reply: () => ({ kind: 'message', content: [{ type: 'text', text: 'built-in' }], stop_reason: 'end_turn', eventDelayMs: 0 }),
}

describe('order', () => {
  test('within a directory, files are tried in code-unit order of their names', async () => {
    const dir = await directory({ 'b.json': says('b'), '2.json': says('2'), 'a.json': says('a'), 'C.json': says('C'), '10.json': says('10') })
    // A locale's order would put `C` after `b`.
    expect((await readScripts([dir])).map(script => script.origin)).toEqual(['10.json', '2.json', 'C.json', 'a.json', 'b.json'].map(name => join(dir, name)))
    expect(textOf(await selectReply([dir], [], HELLO))).toBe('10')
  })

  test('directories are tried in the order given', async () => {
    const one = await directory({ 'z.json': says('one') })
    const two = await directory({ 'a.json': says('two') })
    expect(textOf(await selectReply([one, two], [], HELLO))).toBe('one')
    expect(textOf(await selectReply([two, one], [], HELLO))).toBe('two')
  })

  test('a consumer script comes before a built-in, which answers when no consumer script matches', async () => {
    const dir = await directory({ 'only-goodbye.json': says('consumer', { lastUserContains: ['goodbye'] }) })
    const bye = request({ role: 'user', content: 'goodbye' })
    expect(matched(await selectReply([dir], [EVERYTHING], bye)).origin).toBe(join(dir, 'only-goodbye.json'))
    expect(matched(await selectReply([dir], [EVERYTHING], HELLO)).origin).toBe('built-in: everything')
  })

  test('only .json entries that are not hidden are scripts', async () => {
    const dir = await directory({ 'notes.txt': 'not a script', 'half.json.tmp': '{"match":', '._a.json': '\u0000\u0005', 'a.json': says('a') })
    // An editor's lock file: a dangling symlink beside the file it locks.
    await symlink('user@host.1234', join(dir, '.#a.json'))
    expect((await readScripts([dir])).map(script => script.origin)).toEqual([join(dir, 'a.json')])
  })
})

describe('reading on every request', () => {
  test('a script written or changed after the first request answers the next one', async () => {
    const dir = await directory()
    expect(await selectReply([dir], [], HELLO)).toEqual({ kind: 'unmatched', tried: [] })
    await writeScript(dir, 'late.json', says('late'))
    expect(textOf(await selectReply([dir], [], HELLO))).toBe('late')
    await writeScript(dir, 'late.json', says('changed'))
    expect(textOf(await selectReply([dir], [], HELLO))).toBe('changed')
  })
})

describe('matching', () => {
  const earlier = request(
    { role: 'user', content: 'run the tests' },
    { role: 'assistant', content: [{ type: 'tool_use', id: 'toolu_abc', name: 'Bash', input: { command: 'bun test' } }] },
    { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_abc', content: [{ type: 'text', text: '12 pass' }] }] },
  )

  test('lastUserContains reads the last user message only; bodyContains reads the whole body', async () => {
    const dir = await directory({
      'a.json': says('earlier turn', { lastUserContains: ['run the tests'] }),
      'b.json': says('anywhere', { bodyContains: ['run the tests'] }),
    })
    expect(textOf(await selectReply([dir], [], earlier))).toBe('anywhere')
  })

  test('lastUserContains matches a tool result, and not its id or block type', async () => {
    const dir = await directory({
      'a.json': says('id', { lastUserContains: ['toolu_abc'] }),
      'b.json': says('type', { lastUserContains: ['tool_result'] }),
      'c.json': says('result', { lastUserContains: ['12 pass'] }),
    })
    expect(textOf(await selectReply([dir], [], earlier))).toBe('result')
  })

  test('a lastUserContains needle must fall within one text, not across two', async () => {
    const two = request({ role: 'user', content: [{ type: 'text', text: 'build ok' }, { type: 'text', text: 'deploy' }] })
    const dir = await directory({ 'a.json': says('across', { lastUserContains: ['ok\ndeploy'] }), 'b.json': says('each', { lastUserContains: ['ok', 'deploy'] }) })
    expect(textOf(await selectReply([dir], [], two))).toBe('each')
  })

  test('every needle must appear', async () => {
    const dir = await directory({ 'a.json': says('both', { bodyContains: ['Bash', 'missing'] }) })
    expect(await selectReply([dir], [], earlier)).toEqual({ kind: 'unmatched', tried: [join(dir, 'a.json')] })
  })

  test('a script with no conditions answers every request', async () => {
    const dir = await directory({ 'a.json': says('always') })
    expect(textOf(await selectReply([dir], [], earlier))).toBe('always')
    expect(textOf(await selectReply([dir], [], HELLO))).toBe('always')
  })

  test('no match reports every script tried, in order', async () => {
    const one = await directory({ 'b.json': says('b', { bodyContains: ['nope'] }), 'a.json': says('a', { bodyContains: ['nope'] }) })
    const builtIn: Script = { origin: 'built-in: never', reply: () => null }
    expect(await selectReply([one], [builtIn], HELLO)).toEqual({
      kind: 'unmatched',
      tried: [join(one, 'a.json'), join(one, 'b.json'), 'built-in: never'],
    })
  })
})

describe('the reply', () => {
  test('a message reply takes its defaults, and a literal reply is kept as written', async () => {
    const dir = await directory({
      'a.json': { match: { lastUserContains: ['tool'] }, reply: { kind: 'message', content: [{ type: 'thinking', thinking: 'hm' }, { type: 'tool_use', name: 'Bash', input: { command: 'ls' } }], stop_reason: 'tool_use' } },
      'b.json': { reply: { kind: 'literal', status: 429, headers: { 'retry-after': '1' }, body: '{"type":"error"}' } },
    })
    expect(matched(await selectReply([dir], [], request({ role: 'user', content: 'use a tool' }))).reply).toEqual({
      kind: 'message',
      content: [{ type: 'thinking', thinking: 'hm' }, { type: 'tool_use', name: 'Bash', input: { command: 'ls' } }],
      stop_reason: 'tool_use',
      eventDelayMs: 0,
    })
    expect(matched(await selectReply([dir], [], HELLO)).reply).toEqual({ kind: 'literal', status: 429, headers: { 'retry-after': '1' }, body: '{"type":"error"}' })
  })

  test('max_tokens may cut off a reply with or without a tool call', async () => {
    const dir = await directory({
      'a.json': { reply: { kind: 'message', content: [{ type: 'text', text: 'cut' }], stop_reason: 'max_tokens' } },
      'b.json': { reply: { kind: 'message', content: [{ type: 'tool_use', name: 'Bash', input: {} }], stop_reason: 'max_tokens' } },
    })
    expect(await readScripts([dir])).toHaveLength(2)
  })
})

describe('a script that cannot be read fails loudly with its path', () => {
  // Each malformed script, and the words of the reason its error gives after the path.
  const malformed: Record<string, [unknown, string]> = {
    'not JSON': ['{"reply":', 'not JSON'],
    'an unknown key': [{ ...says('x'), extra: true }, 'Unrecognized key: "extra"'],
    'a misspelled match key': [says('x', { lastUser: ['x'] }), 'Unrecognized key: "lastUser"'],
    'an unknown reply kind': [{ reply: { kind: 'stream', body: 'x' } }, 'at reply.kind'],
    'a status on a message': [{ reply: { ...says('x').reply, status: 200 } }, 'Unrecognized key: "status"'],
    'a delay on a literal': [{ reply: { kind: 'literal', status: 500, body: '', eventDelayMs: 5 } }, 'Unrecognized key: "eventDelayMs"'],
    'an empty needle': [says('x', { bodyContains: [''] }), 'at match.bodyContains[0]'],
    'no content': [{ reply: { kind: 'message', content: [], stop_reason: 'end_turn' } }, 'at reply.content'],
    'a tool call that ends the turn': [
      { reply: { kind: 'message', content: [{ type: 'tool_use', name: 'Bash', input: {} }], stop_reason: 'end_turn' } },
      'at reply.stop_reason',
    ],
    'tool_use with no tool call': [{ reply: { kind: 'message', content: [{ type: 'text', text: 'x' }], stop_reason: 'tool_use' } }, 'at reply.stop_reason'],
    'a status Response refuses': [{ reply: { kind: 'literal', status: 101, body: '' } }, 'at reply.status'],
    'a header name Headers refuses': [{ reply: { kind: 'literal', status: 429, headers: { 'retry after': '1' }, body: '' } }, 'Invalid header name'],
    'a header value Headers refuses': [{ reply: { kind: 'literal', status: 429, headers: { 'retry-after': '1\n2' }, body: '' } }, 'invalid value'],
    'a delay no timer holds': [{ reply: { ...says('x').reply, eventDelayMs: 2 ** 31 } }, 'at reply.eventDelayMs'],
    'one header named twice': [
      { reply: { kind: 'literal', status: 429, headers: { 'Retry-After': '1', 'retry-after': '30' }, body: '' } },
      'names a field twice: retry-after',
    ],
    'a body on a 204': [{ reply: { kind: 'literal', status: 204, body: 'x' } }, 'at reply.body'],
    'tool input that is not an object': [
      { reply: { kind: 'message', content: [{ type: 'tool_use', name: 'Bash', input: 'ls' }], stop_reason: 'tool_use' } },
      'at reply.content[0].input',
    ],
  }
  for (const [what, [script, reason]] of Object.entries(malformed)) {
    test(what, async () => {
      const dir = await directory()
      const path = await writeScript(dir, 'bad.json', script)
      const message = await selectReply([dir], [], HELLO).then(
        () => 'resolved',
        (error: Error) => error.message,
      )
      expect(message).toStartWith(`script ${path}: `)
      expect(message).toContain(reason)
    })
  }

  test('a scripts directory that does not exist', async () => {
    const missing = join(root, 'missing')
    await expect(readScripts([missing])).rejects.toThrow(`scripts directory ${missing}: cannot read: `)
  })

  test('a script name that is still there and cannot be read', async () => {
    const dir = await directory()
    await symlink(join(dir, 'nowhere'), join(dir, 'dangling.json'))
    await expect(readScripts([dir])).rejects.toThrow(`script ${join(dir, 'dangling.json')}: cannot read: `)
  })

  test('a .json entry that is not a file', async () => {
    const dir = await directory()
    await mkdir(join(dir, 'sub.json'))
    await expect(readScripts([dir])).rejects.toThrow(`script ${join(dir, 'sub.json')}: cannot read: Error: not a regular file`)
  })

  test('a named pipe, which would wait forever for a writer', async () => {
    const dir = await directory()
    const fifo = join(dir, 'pipe.json')
    expect(Bun.spawnSync(['mkfifo', fifo]).exitCode).toBe(0)
    await expect(readScripts([dir])).rejects.toThrow(`script ${fifo}: cannot read: Error: not a regular file`)
  })
})
