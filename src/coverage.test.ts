import { afterEach, expect, spyOn, test } from 'bun:test'
import { coverageCommand } from './coverage.ts'
import { answer, type Generators } from './dispatch.ts'
import { buildEndpointTable } from './endpoints.ts'
import { exitCode } from './log.ts'

const API = 'api.anthropic.com'

const ROWS = [
  { id: 'answered', host: API, method: 'GET', path: '/answered', scope: 'in-scope' },
  { id: 'ungenerated', host: API, method: 'POST', path: '/ungenerated/{name}', scope: 'in-scope' },
  { id: 'refused', host: API, method: 'GET', path: '/refused', scope: 'out-of-scope', reason: 'a test reason' },
] as const

const TABLE = buildEndpointTable(ROWS)
const GENERATORS: Generators<(typeof ROWS)[number]> = { answered: () => answer({}, {}) }

// The listing the command writes on stdout, captured instead of printed.
function capturedStdout() {
  let written = ''
  spyOn(process.stdout, 'write').mockImplementation(chunk => {
    written += String(chunk)
    return true
  })
  return () => written
}

afterEach(() => {
  ;(process.stdout.write as unknown as { mockRestore(): void }).mockRestore()
})

test('an in-scope row with no generator: the answer is no (exit 1), and the row is named', async () => {
  const stdout = capturedStdout()
  const result = await coverageCommand(TABLE, GENERATORS).run([])

  expect(result).toEqual({ outcome: 'no', facts: { answered: 1, out_of_scope: 1, no_generator: ['ungenerated'] } })
  expect(exitCode(result)).toBe(1)
  const lines = stdout().trimEnd().split('\n')
  expect(lines.map(line => line.split(/ {2,}/))).toEqual([
    ['answered', 'GET', `${API}/answered`, 'answered'],
    ['no generator', 'POST', `${API}/ungenerated/{name}`, 'ungenerated'],
    ['out of scope', 'GET', `${API}/refused`, 'refused: a test reason'],
  ])
})

test('every in-scope row answered: the answer is ok (exit 0)', async () => {
  capturedStdout()
  const generators: Generators<(typeof ROWS)[number]> = { ...GENERATORS, ungenerated: () => answer({}, {}) }
  const result = await coverageCommand(TABLE, generators).run([])

  expect(result).toEqual({ outcome: 'ok', facts: { answered: 2, out_of_scope: 1, no_generator: [] } })
  expect(exitCode(result)).toBe(0)
})
