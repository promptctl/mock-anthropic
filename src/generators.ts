import type { Account } from './account.ts'
import { accountGenerators } from './account-generators.ts'
import { BUILT_INS } from './built-ins.ts'
import type { Generators } from './dispatch.ts'
import type { Row } from './endpoints.ts'
import { messagesGenerator } from './messages-generator.ts'

// [LAW:one-source-of-truth] The generators the mock answers with: what `serve` hands the server, and
// what `coverage` checks the endpoint table against. Each is built from the two declared inputs
// (PROJECT.md "Inputs"): the account, and the consumer's script directories in the order given.
export function mockGenerators(inputs: { readonly account: Account; readonly scenarioDirs: readonly string[] }): Generators<Row> {
  return { messages: messagesGenerator(inputs.scenarioDirs, BUILT_INS), ...accountGenerators(inputs.account) }
}
