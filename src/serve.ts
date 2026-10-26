import { resolve } from 'node:path'
import { DEFAULT_ACCOUNT, readAccountFile } from './account.ts'
import { CERTS_DIR } from './certificates.ts'
import { type Command, parse, UsageError } from './command.ts'
import { ENDPOINT_TABLE } from './endpoints.ts'
import { mockGenerators } from './generators.ts'
import { eventLine } from './log.ts'
import { startServer } from './server.ts'

// PROJECT.md "Commands": `serve` starts the mock and runs until it is told to stop. Its one stdout line
// is how a consumer finds it; everything else it says goes to stderr.

const MAX_PORT = 65535

export const serve: Command = {
  usage: '[--port N] [--scenarios DIR]... [--account FILE]',
  async run(args, ended) {
    const flags = parse(args, {
      port: { type: 'string', default: '0' },
      scenarios: { type: 'string', multiple: true, default: [] },
      account: { type: 'string' },
    })
    const port = portNumber(flags.port)
    // Absolute, so a FAILED line names the directory whatever the reader's working directory.
    const scenarios = flags.scenarios.map(dir => resolve(dir))
    const accountFile = flags.account === undefined ? null : resolve(flags.account)
    const account = accountFile === null ? DEFAULT_ACCOUNT : await readAccountFile(accountFile)
    const server = await startServer({
      port,
      table: ENDPOINT_TABLE,
      generators: mockGenerators({ account, scenarioDirs: scenarios }),
      certsDir: CERTS_DIR,
      emit: event => process.stderr.write(`${eventLine(event)}\n`),
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

function portNumber(text: string): number {
  if (!/^\d{1,5}$/.test(text) || Number(text) > MAX_PORT) throw new UsageError(`--port ${JSON.stringify(text)}: not a port number from 0 to ${MAX_PORT}`)
  return Number(text)
}
