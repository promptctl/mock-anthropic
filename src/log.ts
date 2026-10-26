import type { Facts } from './dispatch.ts'
import type { ServerEvent } from './server.ts'

// PROJECT.md "Failure behavior" and "Commands": the records the mock writes on stderr, one line each,
// and the exit code an invocation ends with.

// What a command returns: the answer, and what it learned on the way. `exited` is `run`'s: it exits
// with its child's code (PROJECT.md "Commands").
export type CommandResult = { readonly facts: Facts } & (
  | { readonly outcome: 'ok' | 'no' }
  | { readonly outcome: 'exited'; readonly code: number }
)

// How an invocation ended: what its command returned, or why it has no answer. `facts` are the
// command's when it returned, and `{}` when it threw or never ran.
export type Ending = CommandResult | { readonly facts: Facts; readonly outcome: 'usage' | 'failed'; readonly why: string }

// [LAW:nothing-unseen] One record per invocation, whatever ended it, with the exit code it ended with.
export type CommandEvent = {
  readonly event: 'command'
  readonly argv: readonly string[]
  readonly duration_ms: number
  readonly exit: number
} & Ending

// [LAW:one-source-of-truth] The one statement of PROJECT.md's exit codes: 0 success, 1 the answer is
// "no" or the mock failed, 2 a wrong command line, and a child's own code for `run`.
export function exitCode(ending: Ending): number {
  switch (ending.outcome) {
    case 'ok':
      return 0
    case 'no':
    case 'failed':
      return 1
    case 'usage':
      return 2
    case 'exited':
      return ending.code
  }
}

// The line written to stderr for a record: its outcome in capitals, so a consumer can fail on any line
// that starts with UNMATCHED, REFUSED or FAILED, then the whole record as JSON.
// [LAW:types-are-the-program] Only the declared records become a line, so every line opens with an
// outcome the compiler knows.
export function eventLine(event: ServerEvent | CommandEvent): string {
  return `${event.outcome.toUpperCase()} ${JSON.stringify(event)}`
}
