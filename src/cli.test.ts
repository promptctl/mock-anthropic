import { describe, expect, test } from 'bun:test'
import net from 'node:net'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { DEFAULT_ACCOUNT, syntheticUuid } from './account.ts'
import { scriptDirectories, writeScript } from './testing.ts'

// The command line as a consumer meets it: `bun src/cli.ts`, spawned, read through its stdout and
// stderr, and judged by its exit code.

const CLI = fileURLToPath(new URL('./cli.ts', import.meta.url))
const COMMAND = fileURLToPath(new URL('./command.ts', import.meta.url))
const { directory } = await scriptDirectories('cli')

type Finished = { exit: number; stdout: string; stderr: string[]; record: Record<string, unknown> & { outcome: string } }

// The last stderr line is the invocation's record; the test reads it back from its JSON.
async function finished(child: Bun.Subprocess<'ignore', 'pipe', 'pipe'>, stdout: Promise<string>): Promise<Finished> {
  const [exit, out, err] = await Promise.all([child.exited, stdout, new Response(child.stderr).text()])
  const stderr = err.trimEnd().split('\n')
  const last = stderr.at(-1) ?? ''
  const record = JSON.parse(last.slice(last.indexOf(' ') + 1))
  expect(last.split(' ', 1)[0]).toBe(record.outcome.toUpperCase())
  return { exit, stdout: out, stderr, record }
}

function spawn(command: readonly string[]) {
  return Bun.spawn(['bun', ...command], { stdin: 'ignore', stdout: 'pipe', stderr: 'pipe' })
}

async function run(...args: string[]): Promise<Finished> {
  const child = spawn([CLI, ...args])
  return finished(child, new Response(child.stdout).text())
}

// A running `serve`: the parsed ready line, and a stop that sends SIGTERM and returns how it finished.
// The stdout reader keeps reading after the first line, so the test sees anything else written there.
async function serve(...args: string[]) {
  const child = spawn([CLI, 'serve', ...args])
  const reader = child.stdout.getReader()
  let text = ''
  while (!text.includes('\n')) {
    const chunk = await reader.read()
    if (chunk.done) throw new Error(`serve exited before its ready line: ${await new Response(child.stderr).text()}`)
    text += new TextDecoder().decode(chunk.value)
  }
  const firstLine = text.slice(0, text.indexOf('\n'))
  const rest = (async () => {
    let more = text
    for (let chunk = await reader.read(); !chunk.done; chunk = await reader.read()) more += new TextDecoder().decode(chunk.value)
    return more
  })()
  const ready = JSON.parse(firstLine) as { port: number; ca: string }
  return {
    firstLine,
    ready,
    origin: `http://127.0.0.1:${ready.port}`,
    stop: () => {
      child.kill('SIGTERM')
      return finished(child, rest)
    },
  }
}

describe('serve', () => {
  test('prints one ready line, answers on its port, and records the run when stopped', async () => {
    const mock = await serve('--port', '0')
    expect(Object.keys(mock.ready).sort()).toEqual(['ca', 'port'])
    expect(mock.ready.port).toBeGreaterThan(0)
    expect(await Bun.file(mock.ready.ca).exists()).toBe(true)

    const profile = await fetch(`${mock.origin}/api/oauth/profile`)
    expect(profile.status).toBe(200)
    expect(((await profile.json()) as { account: { email: string } }).account.email).toBe(DEFAULT_ACCOUNT.emailAddress)

    const stopped = await mock.stop()
    expect(stopped.exit).toBe(0)
    expect(stopped.stdout).toBe(`${mock.firstLine}\n`)
    expect(stopped.stderr.some(line => line.startsWith('SERVED ') && line.includes('"endpoint":"oauth-profile"'))).toBe(true)
    expect(stopped.record).toMatchObject({
      event: 'command',
      argv: ['serve', '--port', '0'],
      outcome: 'ok',
      exit: 0,
      facts: { port: mock.ready.port, ca: mock.ready.ca, account_file: null, scenarios: [], ended: 'SIGTERM' },
    })
    const { certificates } = stopped.record.facts as { certificates: Record<string, string> }
    expect(Object.keys(certificates).sort()).toEqual(['ca', 'leaf'])
    for (const outcome of Object.values(certificates)) expect(['generated', 'reused', 'lost-race']).toContain(outcome)
  })

  test('answers with the account file and the script directories it is given, in order', async () => {
    const account = { ...DEFAULT_ACCOUNT, accountUuid: syntheticUuid(101), emailAddress: 'override@mock-anthropic.invalid' }
    const inputs = await directory()
    const accountFile = await writeScript(inputs, 'account.json', account)
    const reply = (text: string) => ({ reply: { kind: 'message', content: [{ type: 'text', text }], stop_reason: 'end_turn' } })
    const first = await directory({ 'a.json': reply('from the first directory') })
    const second = await directory({ 'a.json': reply('from the second directory') })
    const mock = await serve('--account', accountFile, '--scenarios', first, '--scenarios', second)

    const profile = (await (await fetch(`${mock.origin}/api/oauth/profile`)).json()) as { account: { email: string; uuid: string } }
    expect(profile.account).toMatchObject({ email: account.emailAddress, uuid: account.accountUuid })
    const body = JSON.stringify({ model: 'claude-test', max_tokens: 16, messages: [{ role: 'user', content: 'hi' }] })
    const message = await fetch(`${mock.origin}/v1/messages`, { method: 'POST', body })
    expect(((await message.json()) as { content: { text: string }[] }).content[0]?.text).toBe('from the first directory')

    const stopped = await mock.stop()
    expect(stopped.exit).toBe(0)
    expect(stopped.record).toMatchObject({ outcome: 'ok', facts: { account_file: accountFile, scenarios: [first, second] } })
  })

  test('a port it cannot listen on fails the start: exit 1, a FAILED record, nothing on stdout', async () => {
    const holder = net.createServer()
    await new Promise<void>(resolve => holder.listen(0, '127.0.0.1', resolve))
    const { port } = holder.address() as net.AddressInfo
    try {
      const result = await run('serve', '--port', String(port))
      expect(result.exit).toBe(1)
      expect(result.stdout).toBe('')
      expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, facts: {} })
    } finally {
      holder.close()
    }
  })
})

describe('a wrong command line', () => {
  const cases: [string, string[], string][] = [
    ['no command', [], 'no command given'],
    ['an unknown command', ['frobnicate'], 'unknown command: frobnicate'],
    ['an unknown flag', ['serve', '--nope'], '--nope'],
    ['a positional', ['serve', 'extra'], 'extra'],
    ['a port that is not a number', ['serve', '--port', 'abc'], '"abc"'],
    ['a port out of range', ['serve', '--port', '70000'], '"70000"'],
    ['a missing account file', ['serve', '--account', '/nonexistent/account.json'], '/nonexistent/account.json'],
  ]
  for (const [what, args, named] of cases) {
    test(`${what}: exit 2, a USAGE record naming the cause, nothing on stdout`, async () => {
      const result = await run(...args)
      expect(result.exit).toBe(2)
      expect(result.stdout).toBe('')
      expect(result.record).toMatchObject({ outcome: 'usage', exit: 2, argv: args, facts: {} })
      expect(result.record.why).toContain(named)
    })
  }

  test('an account file that is not an account: exit 2, naming the file and the field', async () => {
    const file = await writeScript(await directory(), 'account.json', { ...DEFAULT_ACCOUNT, emailAddress: 'not an email' })
    const result = await run('serve', '--account', file)
    expect(result.exit).toBe(2)
    expect(result.record.why).toContain(file)
    expect(result.record.why).toContain('emailAddress')
  })
})

// The dispatcher run on a table of stand-in commands, in a script of its own, so a command can do what
// no real one should: raise an error outside its call chain, or return a child's exit code.
async function dispatch(name: string, body: string): Promise<Finished & { cleanedUp: boolean }> {
  const dir = await directory()
  const script = join(dir, 'main.ts')
  await Bun.write(
    script,
    `import { main } from ${JSON.stringify(COMMAND)}
const marker = ${JSON.stringify(join(dir, 'cleaned-up'))}
process.exitCode = await main({ ${name}: { usage: '', run: async (args, ended) => { ${body} } } }, [${JSON.stringify(name)}])
`,
  )
  const child = spawn([script])
  const result = await finished(child, new Response(child.stdout).text())
  return { ...result, cleanedUp: await Bun.file(join(dir, 'cleaned-up')).exists() }
}

describe('the dispatcher', () => {
  const crashes: [string, string, string][] = [
    ['an error reported with reportError, as a failing sink is', `setTimeout(() => reportError(new Error('the sink broke')), 10)`, 'the sink broke'],
    ['a rejection nobody handled', `setTimeout(() => { void Promise.reject(new Error('a lost promise')) }, 10)`, 'a lost promise'],
  ]
  for (const [what, raise, message] of crashes) {
    test(`${what} ends the run as failed, after the command cleans up, keeping its facts`, async () => {
      const result = await dispatch(
        'waits',
        `${raise}
const reason = await ended
await Bun.write(marker, 'yes')
return { outcome: 'ok', facts: { ended: 'crashed' in reason ? 'crashed' : reason.signal } }`,
      )
      expect(result.exit).toBe(1)
      expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, facts: { ended: 'crashed' } })
      expect(result.record.why).toContain(message)
      expect(result.stderr.filter(line => line.startsWith('FAILED '))).toHaveLength(1)
      expect(result.cleanedUp).toBe(true)
    })
  }

  test("a child's exit code is the run's", async () => {
    const result = await dispatch('child', `return { outcome: 'exited', code: 3, facts: { child: 'stand-in' } }`)
    expect(result.exit).toBe(3)
    expect(result.record).toMatchObject({ outcome: 'exited', code: 3, exit: 3, facts: { child: 'stand-in' } })
  })

  test('a command that throws: exit 1 and a FAILED record carrying the stack', async () => {
    const result = await dispatch('throws', `throw new Error('the command broke')`)
    expect(result.exit).toBe(1)
    expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, facts: {} })
    expect(result.record.why).toContain('the command broke')
    expect(result.record.why).toContain('main.ts')
  })
})
