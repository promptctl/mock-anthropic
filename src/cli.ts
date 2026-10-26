import { DEFAULT_ACCOUNT } from './account.ts'
import { type Commands, main } from './command.ts'
import { coverageCommand } from './coverage.ts'
import { ENDPOINT_TABLE } from './endpoints.ts'
import { mockGenerators } from './generators.ts'
import { serve } from './serve.ts'

// PROJECT.md "Commands": the mock's command line, `bun src/cli.ts <command> [args]`.

// `coverage` asks of the generators `serve` answers with. mockGenerators' type, Required<Generators<Row>>,
// holds every key whatever the account and script directories, so the default inputs answer for all.
export const COMMANDS: Commands = {
  serve,
  coverage: coverageCommand(ENDPOINT_TABLE, mockGenerators({ account: DEFAULT_ACCOUNT, scenarioDirs: [] })),
}

if (import.meta.main) process.exitCode = await main(COMMANDS, process.argv.slice(2))
