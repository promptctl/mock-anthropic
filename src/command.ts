import { parseArgs, type ParseArgsOptionsConfig } from 'node:util'
import { AccountInputError } from './account.ts'
import { type CommandEvent, type CommandResult, type Ending, eventLine, exitCode } from './log.ts'

// PROJECT.md "Commands": the dispatcher every command runs under. It owns what a command does not: the
// invocation's lifetime, its one record, and its exit code.

// Why an invocation was asked to end: a signal, or errors raised outside any command's call chain.
export type EndReason = { readonly signal: 'SIGINT' | 'SIGTERM' } | { readonly crashed: readonly unknown[] }

// A command reads its arguments and returns its result. One that runs until told to stop (serve)
// awaits `ended`; one that finishes on its own never reads it.
export type Command = {
  readonly usage: string
  readonly run: (args: readonly string[], ended: Promise<EndReason>) => Promise<CommandResult>
}

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
  const ending = await invoke(commands, argv)
  const event: CommandEvent = { event: 'command', argv, duration_ms: Math.round(performance.now() - started), exit: exitCode(ending), ...ending }
  process.stderr.write(`${eventLine(event)}\n`)
  return event.exit
}

async function invoke(commands: Commands, argv: readonly string[]): Promise<Ending> {
  const [name, ...args] = argv
  const command = name !== undefined && Object.hasOwn(commands, name) ? commands[name] : undefined
  if (command === undefined) return usage(commands, name === undefined ? 'no command given' : `unknown command: ${name}`)
  const lifetime = watch()
  try {
    const result = await command.run(args, lifetime.ended)
    const crashed = lifetime.crashed()
    return crashed.length === 0 ? result : { outcome: 'failed', why: crashed.map(describe).join('\n'), facts: result.facts }
  } catch (error) {
    if (error instanceof UsageError || error instanceof AccountInputError) return usage(commands, error.message)
    return { outcome: 'failed', why: [...lifetime.crashed(), error].map(describe).join('\n'), facts: {} }
  } finally {
    lifetime.close()
  }
}

function usage(commands: Commands, why: string): Ending {
  const lines = Object.entries(commands).map(([name, command]) => `  ${name} ${command.usage}`)
  process.stderr.write(`mock-anthropic: ${why}\nusage: bun src/cli.ts <command>\n${lines.join('\n')}\n`)
  return { outcome: 'usage', why, facts: {} }
}

// [LAW:no-ambient-temporal-coupling] The invocation's lifetime, owned here: the first SIGINT or SIGTERM,
// or the first error nobody caught (a sink's failure is reported with reportError, server.ts), ends it.
// Every such error is kept for the record, and the listeners last exactly as long as the command.
function watch(): { readonly ended: Promise<EndReason>; crashed(): readonly unknown[]; close(): void } {
  const crashes: unknown[] = []
  let end!: (reason: EndReason) => void
  const ended = new Promise<EndReason>(resolve => {
    end = resolve
  })
  const onInterrupt = () => end({ signal: 'SIGINT' })
  const onTerminate = () => end({ signal: 'SIGTERM' })
  const onCrash = (error: unknown) => {
    crashes.push(error)
    end({ crashed: crashes })
  }
  process.once('SIGINT', onInterrupt)
  process.once('SIGTERM', onTerminate)
  process.on('uncaughtException', onCrash)
  process.on('unhandledRejection', onCrash)
  return {
    ended,
    crashed: () => crashes,
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
