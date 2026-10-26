import { stat } from 'node:fs/promises'
import { resolve } from 'node:path'
import { type Account, AccountInputError, DEFAULT_ACCOUNT, readAccountFile } from './account.ts'
import { CERTS_DIR } from './certificates.ts'
import { type Command, parse, UsageError } from './command.ts'
import { ENDPOINT_TABLE } from './endpoints.ts'
import { mockGenerators } from './generators.ts'
import { writeRecord } from './log.ts'
import { parsePort, startServer } from './server.ts'

// PROJECT.md "Commands": `serve` starts the mock and runs until it is told to stop. Its one stdout line
// is how a consumer finds it; everything else it says goes to stderr.

export const serve: Command = {
  kind: 'service',
  usage: '[--port N] [--scenarios DIR]... [--account FILE]',
  async run(args, ended) {
    const flags = parse(args, {
      port: { type: 'string', default: '0' },
      scenarios: { type: 'string', multiple: true, default: [] },
      account: { type: 'string' },
    })
    const port = parsePort(flags.port)
    if (port === null) throw new UsageError(`--port ${JSON.stringify(flags.port)}: not a port number from 0 to 65535`)
    const scenarios = await Promise.all(flags.scenarios.map(scenarioDirectory))
    const accountFile = flags.account === undefined ? null : commandLinePath('account', flags.account)
    const account = await accountFrom(accountFile)
    const server = await startServer({
      port,
      table: ENDPOINT_TABLE,
      generators: mockGenerators({ account, scenarioDirs: scenarios }),
      certsDir: CERTS_DIR,
      emit: writeRecord,
    })
    const { ca, leaf } = server.certificates
    process.stdout.write(`${JSON.stringify({ port: server.port, ca: ca.certPath })}\n`)
    const reason = await ended
    await server.stop()
    return {
      outcome: 'ok',
      // [LAW:nothing-unseen] Which account won (null: the default), where each certificate came from,
      // and what ended the run.
      facts: {
        port: server.port,
        ca: ca.certPath,
        account_file: accountFile,
        scenarios,
        certificates: { ca: ca.outcome, leaf: leaf.outcome },
        ended: 'signal' in reason ? reason.signal : 'crashed',
      },
    }
  },
}

// A path given on the command line, made absolute so a record names it whatever the reader's working
// directory. An empty one (an unset shell variable) is a wrong command line, not the working directory.
function commandLinePath(flag: string, text: string): string {
  if (text === '') throw new UsageError(`--${flag}: an empty path`)
  return resolve(text)
}

// Its scripts are read per request (scripts.ts), so one written after the start still answers; a
// directory that is not there at the start is a wrong command line.
async function scenarioDirectory(dir: string): Promise<string> {
  const path = commandLinePath('scenarios', dir)
  const found = await stat(path).catch((error: unknown) => {
    throw new UsageError(`--scenarios ${path}: ${error instanceof Error ? error.message : String(error)}`, { cause: error })
  })
  if (!found.isDirectory()) throw new UsageError(`--scenarios ${path}: not a directory`)
  return path
}

// An account file that cannot be read or is not an account is a wrong command line.
async function accountFrom(file: string | null): Promise<Account> {
  if (file === null) return DEFAULT_ACCOUNT
  return readAccountFile(file).catch((error: unknown) => {
    throw error instanceof AccountInputError ? new UsageError(error.message, { cause: error }) : error
  })
}
