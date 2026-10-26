import { stat } from 'node:fs/promises'
import { resolve } from 'node:path'
import { type Account, AccountInputError, DEFAULT_ACCOUNT, readAccountFile } from './account.ts'
import { UsageError } from './command.ts'

// The inputs more than one command reads off its command line: a path, and the account.

// A path given on the command line, made absolute so a record names it whatever the reader's working
// directory. An empty one (an unset shell variable) is a wrong command line, not the working directory.
// `given` names where on the command line it came from: `--account`, `<config-dir>`.
export function commandLinePath(given: string, text: string): string {
  if (text === '') throw new UsageError(`${given}: an empty path`)
  return resolve(text)
}

// A command-line path that must name a directory there now; anything else is a wrong command line.
export async function commandLineDirectory(given: string, text: string): Promise<string> {
  const path = commandLinePath(given, text)
  const found = await stat(path).catch((error: unknown) => {
    throw new UsageError(`${given} ${path}: ${error instanceof Error ? error.message : String(error)}`, { cause: error })
  })
  if (!found.isDirectory()) throw new UsageError(`${given} ${path}: not a directory`)
  return path
}

// An account file that cannot be read or is not an account is a wrong command line; no file, the
// default account.
export async function accountFrom(file: string | null): Promise<Account> {
  if (file === null) return DEFAULT_ACCOUNT
  return readAccountFile(file).catch((error: unknown) => {
    throw error instanceof AccountInputError ? new UsageError(error.message, { cause: error }) : error
  })
}
