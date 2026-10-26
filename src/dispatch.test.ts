import { describe, expect, test } from 'bun:test'
import { type Generators, dispatch, type MockRequest } from './dispatch.ts'
import { buildEndpointTable } from './endpoints.ts'

const API = 'api.anthropic.com'

const ROWS = [
  { id: 'served', host: API, method: 'GET', path: '/served/{name}', scope: 'in-scope' },
  { id: 'declines', host: API, method: 'POST', path: '/declines', scope: 'in-scope' },
  { id: 'throws', host: API, method: 'GET', path: '/throws', scope: 'in-scope' },
  { id: 'rejects', host: API, method: 'GET', path: '/rejects', scope: 'in-scope' },
  { id: 'ungenerated', host: API, method: 'GET', path: '/ungenerated', scope: 'in-scope' },
  { id: 'refused', host: API, method: 'GET', path: '/refused', scope: 'out-of-scope', reason: 'a test reason' },
] as const

type Id = (typeof ROWS)[number]['id']

const TABLE = buildEndpointTable(ROWS)

const GENERATORS: Generators<(typeof ROWS)[number]> = {
  served: ({ params }, request) => ({
    kind: 'response',
    response: new Response(`hello ${params.name} at ${request.url.hostname}`, { status: 201 }),
  }),
  declines: (_, request) => ({ kind: 'declined', why: `nothing matches ${request.body}` }),
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

const run = (method: string, path: string, body?: string) => dispatch(TABLE, GENERATORS, request(method, path, body))

describe('dispatch', () => {
  test("a generator's response is served as it was given, with the row and its params", async () => {
    const { endpoint, verdict, response } = await run('GET', '/served/abc')
    expect(endpoint).toBe('served')
    expect(verdict).toEqual({ outcome: 'served' })
    expect(response.status).toBe(201)
    expect(await response.text()).toBe(`hello abc at ${API}`)
  })

  const unmatched: [string, string, string, string, Id | null, string][] = [
    ['no endpoint', 'GET', '/nowhere', '', null, 'no endpoint'],
    ['an out-of-scope row', 'GET', '/refused', '', 'refused', 'out of scope: a test reason'],
    ['an in-scope row with no generator', 'GET', '/ungenerated', '', 'ungenerated', 'no generator'],
    ['a generator that declines', 'POST', '/declines', 'body-x', 'declines', 'nothing matches body-x'],
  ]
  for (const [name, method, path, body, id, why] of unmatched) {
    test(`${name} is a 400 naming the method, host, path and why`, async () => {
      const { endpoint, verdict, response } = await run(method, path, body)
      expect(endpoint).toBe(id)
      expect(verdict).toEqual({ outcome: 'unmatched', why })
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
      const { verdict, response } = await run('GET', path)
      expect(verdict).toEqual({ outcome: 'failed', why })
      expect(response.status).toBe(500)
      expect(response.headers.get('x-should-retry')).toBe('false')
      expect(await response.json()).toEqual({
        type: 'error',
        error: { type: 'api_error', message: `mock-anthropic FAILED: GET ${API} ${path}: ${why}` },
      })
    })
  }
})
