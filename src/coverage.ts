import { type Command, parse } from './command.ts'
import { type Generators, generatorFor } from './dispatch.ts'
import type { EndpointRow, EndpointTable } from './endpoints.ts'

// PROJECT.md "Endpoint table": `coverage` lists every endpoint of the table as answered or out of
// scope, and answers "no" when an in-scope endpoint has no generator.

// [LAW:types-are-the-program] Where one row stands. Only an out-of-scope row has a reason, and an
// in-scope row is answered or it is not.
export type Covered = { readonly row: EndpointRow } & (
  | { readonly status: 'answered' | 'no generator' }
  | { readonly status: 'out of scope'; readonly reason: string }
)

// [LAW:effects-at-boundaries] Pure: one entry per row, in table order.
export function coverageOf<R extends EndpointRow>(table: EndpointTable<R>, generators: Generators<R>): readonly Covered[] {
  return table.endpoints.map((row): Covered => {
    if (row.scope === 'out-of-scope') return { row, status: 'out of scope', reason: row.reason }
    return { row, status: generatorFor(generators, row) === undefined ? 'no generator' : 'answered' }
  })
}

// One line per row, for a person to scan: status, method and URL in aligned columns, then the row's id
// and, out of scope, why.
export function listing(coverage: readonly Covered[]): string {
  const lines = coverage.map(covered => {
    const { row } = covered
    const id = covered.status === 'out of scope' ? `${row.id}: ${covered.reason}` : row.id
    return [covered.status, row.method, `${row.host}${row.path}`, id] as const
  })
  const width = (column: 0 | 1 | 2) => Math.max(0, ...lines.map(line => line[column].length))
  const [status, method, url] = [width(0), width(1), width(2)]
  return lines.map(line => `${line[0].padEnd(status)}  ${line[1].padEnd(method)}  ${line[2].padEnd(url)}  ${line[3]}\n`).join('')
}

// The command for a table and the generators that answer it: the listing on stdout, and "no" (exit 1)
// when any in-scope row has no generator.
export function coverageCommand<R extends EndpointRow>(table: EndpointTable<R>, generators: Generators<R>): Command & { readonly kind: 'task' } {
  return {
    kind: 'task',
    usage: '',
    async run(args) {
      parse(args, {}, [])
      const coverage = coverageOf(table, generators)
      process.stdout.write(listing(coverage))
      const ids = (status: Covered['status']) => coverage.filter(covered => covered.status === status).map(covered => covered.row.id)
      const missing = ids('no generator')
      return {
        outcome: missing.length === 0 ? 'ok' : 'no',
        // [LAW:nothing-unseen] Every count, zero included, and which rows have no generator.
        facts: { answered: ids('answered').length, out_of_scope: ids('out of scope').length, no_generator: missing },
      }
    },
  }
}
