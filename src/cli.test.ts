import { afterEach, describe, expect, test } from 'bun:test'
import { chmod, mkdir, stat, symlink } from 'node:fs/promises'
import net from 'node:net'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { DEFAULT_ACCOUNT, syntheticUuid } from './account.ts'
import { ENDPOINT_TABLE } from './endpoints.ts'
import type { Signal } from './log.ts'
import { CONFIG_FILE, CREDENTIALS_FILE, keychainService, LEGACY_CONFIG_FILE } from './login.ts'
import { ACCESS_TOKEN, OAUTH_SCOPES, REFRESH_TOKEN, TOKEN_LIFETIME_SECONDS } from './oauth.ts'
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

// Every child a test starts is killed when the test ends, so one whose assertions failed partway leaves
// no server listening behind it.
const children = new Set<Bun.Subprocess>()
afterEach(() => {
  for (const child of children) child.kill('SIGKILL')
  children.clear()
})

function spawn(command: readonly string[]) {
  const child = Bun.spawn(['bun', ...command], { stdin: 'ignore', stdout: 'pipe', stderr: 'pipe' })
  children.add(child)
  return child
}

// `child`'s stdout read a line at a time, then the rest of it whole.
function stdoutLines(child: Bun.Subprocess<'ignore', 'pipe', 'pipe'>) {
  const reader = child.stdout.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  return {
    async next(): Promise<string> {
      while (!buffer.includes('\n')) {
        const chunk = await reader.read()
        if (chunk.done) throw new Error(`stdout ended before a full line: ${await new Response(child.stderr).text()}`)
        buffer += decoder.decode(chunk.value, { stream: true })
      }
      const end = buffer.indexOf('\n')
      const line = buffer.slice(0, end)
      buffer = buffer.slice(end + 1)
      return line
    },
    async rest(): Promise<string> {
      for (let chunk = await reader.read(); !chunk.done; chunk = await reader.read()) buffer += decoder.decode(chunk.value, { stream: true })
      return buffer
    },
  }
}

async function run(...args: string[]): Promise<Finished> {
  const child = spawn([CLI, ...args])
  return finished(child, new Response(child.stdout).text())
}

// A running `serve`: the parsed ready line, and a stop that sends SIGTERM and returns how it finished,
// with everything written on stdout, ready line included.
async function serve(...args: string[]) {
  const child = spawn([CLI, 'serve', ...args])
  const stdout = stdoutLines(child)
  const line = await stdout.next()
  const ready = JSON.parse(line) as { port: number; ca: string }
  return {
    firstLine: line,
    ready,
    origin: `http://127.0.0.1:${ready.port}`,
    stop: () => {
      child.kill('SIGTERM')
      return finished(child, stdout.rest().then(more => `${line}\n${more}`))
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
      duration_ms: expect.any(Number),
      ended_by: 'SIGTERM',
      facts: { port: mock.ready.port, ca: mock.ready.ca, account_file: null, scenarios: [] },
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

  test('an error nobody caught stops it and fails the run, keeping what it learned', async () => {
    // reportError is how a failing sink reports (server.ts); it lands while serve starts or runs, and
    // either way serve stops on it.
    const script = join(await directory(), 'crash.ts')
    await Bun.write(
      script,
      `import { main } from ${JSON.stringify(COMMAND)}
import { COMMANDS } from ${JSON.stringify(CLI)}
setTimeout(() => reportError(new Error('nobody caught this')), 0)
process.exitCode = await main(COMMANDS, ['serve', '--port', '0'])
`,
    )
    const child = spawn([script])
    const result = await finished(child, new Response(child.stdout).text())
    expect(result.exit).toBe(1)
    expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, ended_by: 'crash', facts: { account_file: null } })
    expect(result.record.why).toContain('nobody caught this')
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

describe('login', () => {
  const read = async (dir: string, name: string) => ({ value: await Bun.file(join(dir, name)).json(), mode: (await stat(join(dir, name))).mode & 0o777 })
  // On darwin a fresh config dir has no keychain entry to remove; elsewhere there is no keychain.
  const keychain = process.platform === 'darwin' ? 0 : null

  test('writes the credential, the account, onboarding and the trusted projects, and records them', async () => {
    const account = { ...DEFAULT_ACCOUNT, accountUuid: syntheticUuid(201), organizationName: 'Override Org', subscriptionType: 'team' }
    const root = await directory()
    const accountFile = await writeScript(root, 'account.json', account)
    const project = join(root, 'project')
    await mkdir(project)
    // A project named through a symlink is trusted under its real path, which Claude Code keys by.
    const linked = join(root, 'linked')
    await symlink(project, linked)
    const real = await Bun.$`realpath ${project}`.text()
    const configDir = join(root, 'config')

    const before = Date.now()
    const result = await run('login', configDir, '--trust', linked, '--account', accountFile)
    const after = Date.now()
    expect(result.exit).toBe(0)
    expect(result.stdout).toBe('')
    expect(result.record).toMatchObject({
      outcome: 'ok',
      ended_by: null,
      facts: { config_dir: configDir, account_file: accountFile, trusted: [real.trim()], files: { [CREDENTIALS_FILE]: 'created', [CONFIG_FILE]: 'created', [LEGACY_CONFIG_FILE]: 'absent' }, keychain_removed: keychain },
    })

    const credentials = await read(configDir, CREDENTIALS_FILE)
    const { expiresAt } = credentials.value.claudeAiOauth
    expect(expiresAt).toBeGreaterThanOrEqual(before + TOKEN_LIFETIME_SECONDS * 1000)
    expect(expiresAt).toBeLessThanOrEqual(after + TOKEN_LIFETIME_SECONDS * 1000)
    expect(credentials).toEqual({
      value: {
        claudeAiOauth: { accessToken: ACCESS_TOKEN, refreshToken: REFRESH_TOKEN, expiresAt, scopes: [...OAUTH_SCOPES], subscriptionType: 'team', rateLimitTier: null },
      },
      mode: 0o600,
    })
    expect(await read(configDir, CONFIG_FILE)).toEqual({
      value: {
        hasCompletedOnboarding: true,
        oauthAccount: { accountUuid: account.accountUuid, emailAddress: account.emailAddress, organizationUuid: account.organizationUuid, organizationName: 'Override Org' },
        projects: { [real.trim()]: { hasTrustDialogAccepted: true } },
      },
      mode: 0o600,
    })
  })

  test('with no account file, logs in as the default account and trusts nothing', async () => {
    const configDir = join(await directory(), 'config')
    const result = await run('login', configDir)
    expect(result.exit).toBe(0)
    expect(result.record).toMatchObject({ outcome: 'ok', facts: { account_file: null, trusted: [], keychain_removed: keychain } })
    expect((await read(configDir, CREDENTIALS_FILE)).value.claudeAiOauth.subscriptionType).toBe(DEFAULT_ACCOUNT.subscriptionType)
    const { accountUuid, emailAddress, organizationUuid, organizationName } = DEFAULT_ACCOUNT
    expect((await read(configDir, CONFIG_FILE)).value).toEqual({
      hasCompletedOnboarding: true,
      oauthAccount: { accountUuid, emailAddress, organizationUuid, organizationName },
      projects: {},
    })
  })

  test('into an existing config dir, replaces both files whole and leaves the rest alone', async () => {
    const configDir = await directory()
    // Cached flags and grove config would outlive a merge and outrank the mock's answers.
    await writeScript(configDir, CONFIG_FILE, { cachedGrowthBookFeatures: { stale: true }, groveConfigCache: { grove_enabled: true }, hasCompletedOnboarding: false })
    await writeScript(configDir, CREDENTIALS_FILE, { claudeAiOauth: { accessToken: 'an earlier token' } })
    await chmod(join(configDir, CREDENTIALS_FILE), 0o644)
    await writeScript(configDir, 'settings.json', { kept: true })
    // An older global config, which 2.1.289 reads in place of .claude.json while it is there.
    await writeScript(configDir, LEGACY_CONFIG_FILE, { hasCompletedOnboarding: false })

    const result = await run('login', configDir)
    expect(result.exit).toBe(0)
    expect(result.record).toMatchObject({ outcome: 'ok', facts: { files: { [CREDENTIALS_FILE]: 'replaced', [CONFIG_FILE]: 'replaced', [LEGACY_CONFIG_FILE]: 'removed' } } })
    const credentials = await read(configDir, CREDENTIALS_FILE)
    expect(credentials.mode).toBe(0o600)
    expect(credentials.value.claudeAiOauth.accessToken).toBe(ACCESS_TOKEN)
    expect(Object.keys((await read(configDir, CONFIG_FILE)).value).sort()).toEqual(['hasCompletedOnboarding', 'oauthAccount', 'projects'])
    expect(await Bun.file(join(configDir, 'settings.json')).json()).toEqual({ kept: true })
    expect((await Array.fromAsync(new Bun.Glob('*').scan({ cwd: configDir, dot: true }))).sort()).toEqual([CONFIG_FILE, CREDENTIALS_FILE, 'settings.json'].sort())
  })

  // Claude Code reads a config dir's keychain entry ahead of its file, so a login removes it.
  test.if(process.platform === 'darwin')("removes the config dir's keychain entry, which would outrank the file", async () => {
    const configDir = join(await directory(), 'config')
    const service = keychainService(configDir)
    const entry = () => Bun.$`security find-generic-password -s ${service}`.quiet().nothrow()
    await Bun.$`security add-generic-password -a mock-anthropic-test -s ${service} -w ${'{"claudeAiOauth":{}}'}`.quiet()
    try {
      const first = await run('login', configDir)
      expect(first.record).toMatchObject({ outcome: 'ok', facts: { keychain_removed: 1 } })
      expect((await entry()).exitCode).toBe(44)
      const second = await run('login', configDir)
      expect(second.record).toMatchObject({ outcome: 'ok', facts: { keychain_removed: 0 } })
    } finally {
      await Bun.$`security delete-generic-password -s ${service}`.quiet().nothrow()
    }
  })

  test.if(process.platform === 'darwin')('a keychain it cannot clear fails the login, naming the exit and the reason', async () => {
    const root = await directory()
    // A `security` ahead of the real one on PATH, failing as a machine with no default keychain does.
    await Bun.write(join(root, 'security'), '#!/bin/sh\necho "no default keychain" >&2\nexit 50\n')
    await chmod(join(root, 'security'), 0o755)
    const child = Bun.spawn(['bun', CLI, 'login', join(root, 'config')], {
      stdin: 'ignore',
      stdout: 'pipe',
      stderr: 'pipe',
      env: { ...process.env, PATH: `${root}:${process.env.PATH}` },
    })
    children.add(child)
    const result = await finished(child, new Response(child.stdout).text())
    expect(result.exit).toBe(1)
    expect(result.record).toMatchObject({ outcome: 'failed', facts: {} })
    expect(result.record.why).toContain('exit 50: no default keychain')
  })

  test('a file it cannot put in place fails the login and leaves no temporary file behind', async () => {
    const configDir = await directory()
    await mkdir(join(configDir, CONFIG_FILE))
    const result = await run('login', configDir)
    expect(result.exit).toBe(1)
    expect(result.record).toMatchObject({ outcome: 'failed', facts: {} })
    expect(await Array.fromAsync(new Bun.Glob('*.tmp').scan({ cwd: configDir, dot: true }))).toEqual([])
  })

  test('a config dir path that names a file: exit 2, naming it', async () => {
    const file = await writeScript(await directory(), 'not-a-dir', 'text')
    const result = await run('login', file)
    expect(result.exit).toBe(2)
    expect(result.record).toMatchObject({ outcome: 'usage', facts: {} })
    expect(result.record.why).toContain(`<config-dir> ${file}: not a directory`)
  })
})

describe('coverage', () => {
  test('lists every endpoint of the table, answers yes on the tree, and records the counts', async () => {
    const result = await run('coverage')
    expect(result.exit).toBe(0)
    const listed = result.stdout.trimEnd().split('\n').map(line => line.split(/ {2,}/))
    const { endpoints } = ENDPOINT_TABLE
    expect(listed.map(([status, method, url]) => [status, method, url])).toEqual(
      endpoints.map(row => [row.scope === 'in-scope' ? 'answered' : 'out of scope', row.method, `${row.host}${row.path}`]),
    )
    expect(listed.map(line => line[3])).toEqual(endpoints.map(row => (row.scope === 'in-scope' ? row.id : `${row.id}: ${row.reason}`)))
    expect(result.record).toMatchObject({
      outcome: 'ok',
      exit: 0,
      argv: ['coverage'],
      ended_by: null,
      facts: {
        answered: endpoints.filter(row => row.scope === 'in-scope').length,
        out_of_scope: endpoints.filter(row => row.scope === 'out-of-scope').length,
        no_generator: [],
      },
    })
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
    ['a missing scripts directory', ['serve', '--scenarios', '/nonexistent/scripts'], '/nonexistent/scripts'],
    ['an empty scripts directory path', ['serve', '--scenarios', ''], '--scenarios: an empty path'],
    ['an empty account file path', ['serve', '--account', ''], '--account: an empty path'],
    ['a flag that takes one value, given twice', ['serve', '--port', '0', '--port', '1'], '--port: given more than once'],
    ['an argument to coverage', ['coverage', 'extra'], 'extra'],
    ['login with no config dir', ['login'], 'missing <config-dir>'],
    ['login with two config dirs', ['login', '/nonexistent/a', '/nonexistent/b'], 'unexpected argument: /nonexistent/b'],
    ['an empty config dir path', ['login', ''], '<config-dir>: an empty path'],
    ['a project to trust that is not there', ['login', '/nonexistent/config', '--trust', '/nonexistent/project'], '--trust /nonexistent/project'],
    ['a login account file that is not there', ['login', '/nonexistent/config', '--account', '/nonexistent/account.json'], '/nonexistent/account.json'],
  ]
  for (const [what, args, named] of cases) {
    test(`${what}: exit 2, a USAGE record naming the cause, nothing on stdout`, async () => {
      const result = await run(...args)
      expect(result.exit).toBe(2)
      expect(result.stdout).toBe('')
      expect(result.record).toMatchObject({ outcome: 'usage', exit: 2, argv: args, ended_by: null, facts: {} })
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

// The dispatcher run on a table of one stand-in command, in a script of its own, so a command can do
// what no real one should: raise an error outside its call chain, ignore a signal, throw before it
// returns a promise, or return a child's exit code. `run` is the command's own, as source; it can write
// `marker` to show it ran to its end, and call `say(line)` so the test knows how far it got.
async function dispatch(kind: 'task' | 'service', run: string) {
  const dir = await directory()
  const script = join(dir, 'main.ts')
  const marker = join(dir, 'cleaned-up')
  await Bun.write(
    script,
    `import { main } from ${JSON.stringify(COMMAND)}
const marker = ${JSON.stringify(marker)}
const say = (line) => process.stdout.write(line + '\\n')
const forever = new Promise(() => {})
process.exitCode = await main({ stand_in: { kind: '${kind}', usage: '', run: ${run} } }, ['stand_in'])
`,
  )
  const child = spawn([script])
  const stdout = stdoutLines(child)
  return {
    // The command's next `say`: once it is read, a signal sent next finds the dispatcher listening.
    said: () => stdout.next(),
    signal: (signal: Signal) => child.kill(signal),
    finished: async () => ({ ...(await finished(child, stdout.rest())), cleanedUp: await Bun.file(marker).exists() }),
  }
}

// An async run with `text` as its body.
const body = (text: string) => `async (args, ended) => { ${text} }`

describe('the dispatcher', () => {
  const crashes: [string, string, string][] = [
    ['an error reported with reportError, as a failing sink is', `setTimeout(() => reportError(new Error('the sink broke')), 10)`, 'the sink broke'],
    ['a rejection nobody handled', `setTimeout(() => { void Promise.reject(new Error('a lost promise')) }, 10)`, 'a lost promise'],
  ]
  for (const [what, raise, message] of crashes) {
    test(`${what} ends a service as failed, after it stops, keeping its facts`, async () => {
      const result = await (
        await dispatch(
          'service',
          body(`${raise}
const reason = await ended
await Bun.write(marker, 'yes')
return { outcome: 'ok', facts: { ended: 'crash' in reason ? 'crashed' : reason.signal } }`),
        )
      ).finished()
      expect(result.exit).toBe(1)
      expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, ended_by: 'crash', facts: { ended: 'crashed' } })
      expect(result.record.why).toContain(message)
      expect(result.stderr.filter(line => line.startsWith('FAILED '))).toHaveLength(1)
      expect(result.cleanedUp).toBe(true)
    })

    test(`${what} ends a task that is stuck, without waiting for it`, async () => {
      const result = await (await dispatch('task', body(`${raise}\nawait forever`))).finished()
      expect(result.exit).toBe(1)
      expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, facts: {} })
      expect(result.record.why).toContain(message)
    })
  }

  // PROJECT.md's signals, listed here so a signal the dispatcher stops hearing fails its test.
  for (const signal of ['SIGHUP', 'SIGINT', 'SIGTERM'] as const) {
    test(`${signal} ends a task at once, with a FAILED record naming it`, async () => {
      const running = await dispatch('task', body(`say('running')\nawait forever`))
      expect(await running.said()).toBe('running')
      running.signal(signal)
      const result = await running.finished()
      expect(result.exit).toBe(1)
      expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, ended_by: signal, why: `${signal} before the command finished` })
    })
  }

  test('a second signal ends a service that is still stopping, with its record', async () => {
    const running = await dispatch('service', body(`say('running')\nawait ended\nsay('stopping')\nawait forever`))
    expect(await running.said()).toBe('running')
    running.signal('SIGINT')
    expect(await running.said()).toBe('stopping')
    running.signal('SIGINT')
    const result = await running.finished()
    expect(result.exit).toBe(1)
    expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, why: 'SIGINT before the command finished\nSIGINT while the command was ending' })
  })

  test('one signal after a crash does not abandon the service it finds stopping', async () => {
    const running = await dispatch(
      'service',
      body(`setTimeout(() => reportError(new Error('the sink broke')), 10)
await ended
const signalled = new Promise(resolve => process.once('SIGTERM', resolve))
say('stopping')
await signalled
await Bun.write(marker, 'yes')
return { outcome: 'ok', facts: { stopped: 'cleanly' } }`),
    )
    expect(await running.said()).toBe('stopping')
    running.signal('SIGTERM')
    const result = await running.finished()
    expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, ended_by: 'crash', facts: { stopped: 'cleanly' } })
    expect(result.record.why).toContain('SIGTERM while the command was ending')
    expect(result.cleanedUp).toBe(true)
  })

  test('the record of an abandoned command reaches a reader that is behind on stderr', async () => {
    // The task writes a MiB of stderr, then signals itself. Its stderr is a pipe a shell reads only after
    // a pause, so when the dispatcher exits Bun still holds most of that MiB, and the record behind it.
    const script = join(await directory(), 'behind.ts')
    await Bun.write(
      script,
      `import { main } from ${JSON.stringify(COMMAND)}
const run = async () => {
  process.stderr.write('x'.repeat(1 << 20) + '\\n')
  process.kill(process.pid, 'SIGTERM')
  return new Promise(() => {})
}
process.exitCode = await main({ stand_in: { kind: 'task', usage: '', run } }, ['stand_in'])
`,
    )
    const child = Bun.spawn(['sh', '-c', 'bun "$0" 2>&1 >/dev/null | { sleep 0.5; cat; }', script], { stdin: 'ignore', stdout: 'pipe', stderr: 'ignore' })
    children.add(child)
    const last = (await new Response(child.stdout).text()).trimEnd().split('\n').at(-1) ?? ''
    expect(last.slice(0, 'FAILED '.length)).toBe('FAILED ')
    expect(JSON.parse(last.slice('FAILED '.length))).toMatchObject({ outcome: 'failed', exit: 1, why: 'SIGTERM before the command finished' })
  })

  test("a child's exit code is the run's", async () => {
    const result = await (await dispatch('task', body(`return { outcome: 'exited', code: 3, facts: { child: 'stand-in' } }`))).finished()
    expect(result.exit).toBe(3)
    expect(result.record).toMatchObject({ outcome: 'exited', code: 3, exit: 3, facts: { child: 'stand-in' } })
  })

  test('a command that throws: exit 1 and a FAILED record carrying the stack', async () => {
    const result = await (await dispatch('task', body(`throw new Error('the command broke')`))).finished()
    expect(result.exit).toBe(1)
    expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, facts: {} })
    expect(result.record.why).toContain('the command broke')
    expect(result.record.why).toContain('main.ts')
  })

  test('a command that throws before it returns a promise is recorded the same way', async () => {
    const result = await (await dispatch('task', `() => { throw new Error('thrown at once') }`)).finished()
    expect(result.exit).toBe(1)
    expect(result.record).toMatchObject({ outcome: 'failed', exit: 1, facts: {} })
    expect(result.record.why).toContain('thrown at once')
  })
})
