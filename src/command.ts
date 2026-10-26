import { parseArgs, type ParseArgsOptionsConfig } from 'node:util'
import type { Facts } from './dispatch.ts'
import { type CommandEvent, type CommandResult, type Ending, eventLine, exitCode } from './log.ts'

// PROJECT.md "Commands": the dispatcher every command runs under. It owns what a command does not: the
// invocation's lifetime, its one record, and its exit code.

// Why an invocation was asked to end: a signal, or errors raised outside any command's call chain.
export type EndReason = { readonly signal: Signal } | { readonly crashed: readonly unknown[] }

type Signal = 'SIGINT' | 'SIGTERM'

// [LAW:types-are-the-program] How a command ends is its kind. A task finishes by itself and never hears
// of a signal or a crash: either ends the invocation at once, without waiting for it. A service runs
// until `ended` and then stops; while it stops, a second signal ends the invocation without it.
export type Command = { readonly usage: string } & (
  | { readonly kind: 'task'; readonly run: (args: readonly string[]) => Promise<CommandResult> }
  | { readonly kind: 'service'; readonly run: (args: readonly string[], ended: Promise<EndReason>) => Promise<CommandResult> }
)

export type Commands = { readonly [name: string]: Command }

// A wrong command line, which the dispatcher answers with exit 2.
export class UsageError extends Error {
  override name = 'UsageError'
}

// [LAW:single-enforcer] The one parse of a command's flags: unknown flags and positionals are refused,
// and every refusal of parseArgs's becomes a UsageError.
export function parse<const O extends ParseArgsOptionsConfig>(args: readonly string[], options: O) {
  try {
    return parseArgs({ args: [...args], options, strict: true, allowPositionals: false }).values
  } catch (error) {
    if (error instanceof TypeError && 'code' in error && String(error.code).startsWith('ERR_PARSE_ARGS_')) throw new UsageError(error.message)
    throw error
  }
}

// [LAW:single-enforcer] [LAW:nothing-unseen] The only place an invocation is recorded and its exit
// code chosen: every invocation, however it ends, writes exactly one command line on stderr and
// returns the code that line carries.
export async function main(commands: Commands, argv: readonly string[]): Promise<number> {
  const started = performance.now()
  const { ending, abandoned } = await invoke(commands, argv)
  const event: CommandEvent = { event: 'command', argv, duration_ms: Math.round(performance.now() - started), exit: exitCode(ending), ...ending }
  process.stderr.write(`${eventLine(event)}\n`)
  // A command that was not waited for is still running, and nothing else will stop it. Otherwise the
  // process ends by itself: process.exit drops piped output Bun has not yet written (Bun 1.3.14 holds
  // it past the pipe's 64 KiB with writableLength 0), so it is kept for the run whose output is cut
  // short anyway.
  if (abandoned) process.exit(event.exit)
  return event.exit
}

type Invoked = { readonly ending: Ending; readonly abandoned: boolean }

async function invoke(commands: Commands, argv: readonly string[]): Promise<Invoked> {
  const [name, ...args] = argv
  const command = name !== undefined && Object.hasOwn(commands, name) ? commands[name] : undefined
  if (command === undefined) return { ending: usage(commands, name === undefined ? 'no command given' : `unknown command: ${name}`), abandoned: false }
  const lifetime = watch(command.kind)
  try {
    const running = command.kind === 'task' ? command.run(args) : command.run(args, lifetime.ended)
    const settled = await Promise.race([
      running.then(
        result => ({ kind: 'returned', result }) as const,
        (error: unknown) => ({ kind: 'threw', error }) as const,
      ),
      lifetime.forced.then(() => ({ kind: 'abandoned' }) as const),
    ])
    // Errors nobody caught outrank whatever the command did after them: they are why it ended.
    const crashed = lifetime.crashed()
    switch (settled.kind) {
      case 'abandoned':
        return { ending: failed(crashed, lifetime.signals().map(signal => `${signal} before the command finished`), {}), abandoned: true }
      case 'returned':
        return { ending: crashed.length === 0 ? settled.result : failed(crashed, [], settled.result.facts), abandoned: false }
      case 'threw':
        if (crashed.length === 0 && settled.error instanceof UsageError) return { ending: usage(commands, settled.error.message), abandoned: false }
        return { ending: failed([...crashed, settled.error], [], {}), abandoned: false }
    }
  } finally {
    lifetime.close()
  }
}

function failed(errors: readonly unknown[], notes: readonly string[], facts: Facts): Ending {
  return { outcome: 'failed', why: [...errors.map(describe), ...notes].join('\n'), facts }
}

function usage(commands: Commands, why: string): Ending {
  const lines = Object.entries(commands).map(([name, command]) => `  ${name} ${command.usage}`)
  process.stderr.write(`mock-anthropic: ${why}\nusage: bun src/cli.ts <command>\n${lines.join('\n')}\n`)
  return { outcome: 'usage', why, facts: {} }
}

// [LAW:no-ambient-temporal-coupling] The invocation's lifetime, owned here. Each SIGINT or SIGTERM, and
// each error nobody caught (a sink's failure is reported with reportError, server.ts), is an end: the
// first resolves `ended`. `forced` resolves when the command can no longer be waited for: at the first
// end for a task, which never hears one, and at a signal after the first end for a service, which is
// stopping. Every end is kept for the record, and the listeners last exactly as long as the command.
function watch(kind: Command['kind']) {
  const crashes: unknown[] = []
  const signals: Signal[] = []
  let ends = 0
  let end!: (reason: EndReason) => void
  let force!: () => void
  const ended = new Promise<EndReason>(resolve => {
    end = resolve
  })
  const forced = new Promise<void>(resolve => {
    force = resolve
  })
  const onEnd = (reason: EndReason) => {
    ends += 1
    end(reason)
    if (kind === 'task' || ('signal' in reason && ends > 1)) force()
  }
  const onSignal = (signal: Signal) => {
    signals.push(signal)
    onEnd({ signal })
  }
  const onInterrupt = () => onSignal('SIGINT')
  const onTerminate = () => onSignal('SIGTERM')
  const onCrash = (error: unknown) => {
    crashes.push(error)
    onEnd({ crashed: crashes })
  }
  process.on('SIGINT', onInterrupt)
  process.on('SIGTERM', onTerminate)
  process.on('uncaughtException', onCrash)
  process.on('unhandledRejection', onCrash)
  return {
    ended,
    forced,
    crashed: (): readonly unknown[] => crashes,
    signals: (): readonly Signal[] => signals,
    close() {
      process.off('SIGINT', onInterrupt)
      process.off('SIGTERM', onTerminate)
      process.off('uncaughtException', onCrash)
      process.off('unhandledRejection', onCrash)
    },
  }
}

function describe(error: unknown): string {
  return error instanceof Error ? (error.stack ?? `${error.name}: ${error.message}`) : String(error)
}
