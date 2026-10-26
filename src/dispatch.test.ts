import { describe, expect, test } from 'bun:test'
import { type Facts, type Generators, dispatch, type MockRequest } from './dispatch.ts'
import { buildEndpointTable, resolveEndpoint } from './endpoints.ts'

const API = 'api.anthropic.com'

const ROWS = [
  { id: 'served', host: API, method: 'GET', path: '/served/{name}', scope: 'in-scope' },
  { id: 'declines', host: API, method: 'POST', path: '/declines', scope: 'in-scope' },
  { id: 'throws', host: API, method: 'GET', path: '/throws', scope: 'in-scope' },
  { id: 'rejects', host: API, method: 'GET', path: '/rejects', scope: 'in-scope' },
  { id: 'ungenerated', host: API, method: 'GET', path: '/ungenerated', scope: 'in-scope' },
  { id: 'refused', host: API, method: 'GET', path: '/refused', scope: 'out-of-scope', reason: 'a test reason' },
] as const

const TABLE = buildEndpointTable(ROWS)

const GENERATORS: Generators<(typeof ROWS)[number]> = {
  served: ({ params }, request) => ({
    kind: 'response',
    response: new Response(`hello ${params.name} at ${request.url.hostname}`, { status: 201 }),
    facts: { greeted: params.name },
  }),
  declines: (_, request) => ({ kind: 'declined', why: `nothing matches ${request.body}`, facts: { tried: ['a', 'b'] } }),
  throws: () => {
    throw new Error('generator broke')
  },
  rejects: async () => Promise.reject(new Error('generator rejected')),
}

const request = (method: string, path: string, body = ''): MockRequest => ({
  method,
  url: new URL(`https://${API}${path}`),
  headers: new Headers(),
  body,
})

const run = (method: string, path: string, body?: string) => {
  const sent = request(method, path, body)
  return dispatch(GENERATORS, resolveEndpoint(TABLE, method, sent.url), sent)
}

describe('dispatch', () => {
  test("a generator's response is served as it was given, with the row's params", async () => {
    const { verdict, response, facts } = await run('GET', '/served/abc')
    expect(verdict).toEqual({ outcome: 'served' })
    expect(facts).toEqual({ greeted: 'abc' })
    expect(response.status).toBe(201)
    expect(await response.text()).toBe(`hello abc at ${API}`)
  })

  // The table's refusals decided nothing a generator reports; a generator that declines keeps its facts.
  const unmatched: [string, string, string, string, string, Facts][] = [
    ['no endpoint', 'GET', '/nowhere', '', 'no endpoint', {}],
    ['an out-of-scope row', 'GET', '/refused', '', 'out of scope: a test reason', {}],
    ['an in-scope row with no generator', 'GET', '/ungenerated', '', 'no generator', {}],
    ['a generator that declines', 'POST', '/declines', 'body-x', 'nothing matches body-x', { tried: ['a', 'b'] }],
  ]
  for (const [name, method, path, body, why, expected] of unmatched) {
    test(`${name} is a 400 naming the method, host, path and why`, async () => {
      const { verdict, response, facts } = await run(method, path, body)
      expect(verdict).toEqual({ outcome: 'unmatched', why })
      expect(facts).toEqual(expected)
      expect(response.status).toBe(400)
      expect(response.headers.get('x-should-retry')).toBe('false')
      expect(await response.json()).toEqual({
        type: 'error',
        error: { type: 'invalid_request_error', message: `mock-anthropic UNMATCHED: ${method} ${API} ${path}: ${why}` },
      })
    })
  }

  for (const [path, why] of [
    ['/throws', 'generator broke'],
    ['/rejects', 'generator rejected'],
  ] as const) {
    test(`a generator that fails (${path}) is a 500 naming it`, async () => {
      const { verdict, response, facts } = await run('GET', path)
      expect(verdict).toEqual({ outcome: 'failed', why })
      expect(facts).toEqual({})
      expect(response.status).toBe(500)
      expect(response.headers.get('x-should-retry')).toBe('false')
      expect(await response.json()).toEqual({
        type: 'error',
        error: { type: 'api_error', message: `mock-anthropic FAILED: GET ${API} ${path}: ${why}` },
      })
    })
  }
})
