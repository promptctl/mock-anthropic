import { CERTS_DIR } from './certificates.ts'
import { type Command, parse, UsageError } from './command.ts'
import { accountFrom, commandLineDirectory, commandLinePath } from './command-line.ts'
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
    const { flags } = parse(
      args,
      {
        port: { type: 'string', default: '0' },
        scenarios: { type: 'string', multiple: true, default: [] },
        account: { type: 'string' },
      },
      [],
    )
    const port = parsePort(flags.port)
    if (port === null) throw new UsageError(`--port ${JSON.stringify(flags.port)}: not a port number from 0 to 65535`)
    // Scripts are read per request (scripts.ts), so one written after the start still answers; a
    // directory that is not there at the start is a wrong command line.
    const scenarios = await Promise.all(flags.scenarios.map(dir => commandLineDirectory('--scenarios', dir)))
    const accountFile = flags.account === undefined ? null : commandLinePath('--account', flags.account)
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
    await ended
    await server.stop()
    return {
      outcome: 'ok',
      // [LAW:nothing-unseen] Which account won (null: the default), and where each certificate came
      // from. What ended the run is the dispatcher's to record.
      facts: {
        port: server.port,
        ca: ca.certPath,
        account_file: accountFile,
        scenarios,
        certificates: { ca: ca.outcome, leaf: leaf.outcome },
      },
    }
  },
}
