import { type Commands, main } from './command.ts'
import { serve } from './serve.ts'

// PROJECT.md "Commands": the mock's command line, `bun src/cli.ts <command> [args]`.

export const COMMANDS: Commands = { serve }

if (import.meta.main) process.exitCode = await main(COMMANDS, process.argv.slice(2))
