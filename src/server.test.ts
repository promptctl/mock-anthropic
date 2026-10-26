import { afterAll, describe, expect, test } from 'bun:test'
import { chmod, mkdtemp, readdir, rm } from 'node:fs/promises'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Facts, Generators } from './dispatch.ts'
import { API, buildEndpointTable } from './endpoints.ts'
import { eventLine, type MockServer, type ServerEvent, startServer } from './server.ts'

const OTHER = 'claude.ai'

const ROWS = [
  { id: 'hello', host: API, method: 'POST', path: '/hello', scope: 'in-scope' },
  { id: 'other-hello', host: OTHER, method: 'GET', path: '/hello', scope: 'in-scope' },
  { id: 'declines', host: API, method: 'GET', path: '/declines', scope: 'in-scope' },
  { id: 'throws', host: API, method: 'GET', path: '/throws', scope: 'in-scope' },
  { id: 'ungenerated', host: API, method: 'GET', path: '/ungenerated', scope: 'in-scope' },
  { id: 'refused', host: API, method: 'GET', path: '/refused', scope: 'out-of-scope', reason: 'a test reason' },
  { id: 'stream', host: API, method: 'GET', path: '/stream', scope: 'in-scope' },
  { id: 'broken', host: API, method: 'GET', path: '/broken', scope: 'in-scope' },
  { id: 'slow', host: API, method: 'GET', path: '/slow', scope: 'in-scope' },
  { id: 'locked', host: API, method: 'GET', path: '/locked', scope: 'in-scope' },
] as const

const TABLE = buildEndpointTable(ROWS)

// What the stream generator reports deciding, which its record keeps however its body ends.
const STREAM_FACTS = { script: 'stream.json', reply: { kind: 'stream' } }

// What a test controls in a running server: when a streamed body may finish or a slow generator may
// answer, whether a stream was cancelled, and when the slow generator began. Each server gets its own,
// so no test waits on another's.
type Controls = { release: () => void; cancelled: Promise<string>; slowBegan: Promise<void> }

function generators(): { generators: Generators<(typeof ROWS)[number]>; controls: Controls } {
  let release!: () => void
  const released = new Promise<void>(resolve => (release = resolve))
  let cancel!: (reason: string) => void
  const cancelled = new Promise<string>(resolve => (cancel = resolve))
  let began!: () => void
  const slowBegan = new Promise<void>(resolve => (began = resolve))
  const respond = (body: string | ReadableStream<Uint8Array>, facts: Facts = {}) => ({ kind: 'response', response: new Response(body), facts }) as const
  return {
    controls: { release, cancelled, slowBegan },
    generators: {
      hello: (_, request) => respond(`hello ${request.url.hostname} ${request.body}`),
      'other-hello': (_, request) => respond(`hello ${request.url.hostname}`),
      declines: () => ({ kind: 'declined', why: 'nothing matches', facts: { tried: ['a'] } }),
      throws: () => {
        throw new Error('generator broke')
      },
      stream: () =>
        respond(
          new ReadableStream({
            async start(controller) {
              controller.enqueue(new TextEncoder().encode('one;'))
              await released
              controller.enqueue(new TextEncoder().encode('two;'))
              controller.close()
            },
            cancel: reason => cancel(String(reason)),
          }),
          STREAM_FACTS,
        ),
      // Answers only once released, with a body that records its cancel.
      slow: async () => {
        began()
        await released
        return respond(new ReadableStream({ cancel: reason => cancel(String(reason)) }))
      },
      // Answers with a body something already reads, which the mock cannot pass on.
      locked: () => {
        const response = new Response('taken')
        void response.body!.getReader()
        return { kind: 'response', response, facts: {} }
      },
      // Fails on the read after its first chunk, as a generator that breaks partway would.
      broken: () => {
        let sent = false
        return respond(
          new ReadableStream({
            pull(controller) {
              if (sent) throw new Error('stream broke')
              sent = true
              controller.enqueue(new TextEncoder().encode('one;'))
            },
          }),
        )
      },
    },
  }
}

const certsDir = await mkdtemp(join(tmpdir(), 'mock-anthropic-server-'))
afterAll(() => rm(certsDir, { recursive: true, force: true }))

type Running = {
  server: MockServer
  controls: Controls
  ca: string
  lines: string[]
  // Resolves with the first line, already emitted or still to come, that starts with `prefix`.
  line: (prefix: string) => Promise<string>
  proxied: (url: string, init?: RequestInit) => Promise<Response>
}

async function start(): Promise<Running> {
  const lines: string[] = []
  const waiters: { prefix: string; resolve: (line: string) => void }[] = []
  const emit = (event: ServerEvent) => {
    const line = eventLine(event)
    lines.push(line)
    for (const waiter of waiters.filter(w => line.startsWith(w.prefix))) {
      waiters.splice(waiters.indexOf(waiter), 1)
      waiter.resolve(line)
    }
  }
  const { generators: g, controls } = generators()
  const server = await startServer({ port: 0, table: TABLE, generators: g, certsDir, emit })
  const ca = await Bun.file(server.certificates.ca.certPath).text()
  return {
    server,
    controls,
    ca,
    lines,
    line: prefix =>
      new Promise(resolve => {
        const found = lines.find(line => line.startsWith(prefix))
        if (found === undefined) waiters.push({ prefix, resolve })
        else resolve(found)
      }),
    // The path Claude Code takes with HTTPS_PROXY and NODE_EXTRA_CA_CERTS: CONNECT, then TLS that
    // trusts only the mock's CA.
    proxied: (url, init) => fetch(url, { ...init, proxy: `http://127.0.0.1:${server.port}`, tls: { ca } }),
  }
}

// The record a line carries, after checking the line's outcome word matches it.
function record(line: string): Record<string, unknown> {
  const [word, ...rest] = line.split(' ')
  const parsed = JSON.parse(rest.join(' ')) as Record<string, unknown>
  expect(word).toBe(String(parsed.outcome).toUpperCase())
  return parsed
}

// Opens a raw connection that sends a request head promising 100 bytes of body and then only four,
// and resolves once the server has started reading that body: its 100 Continue is the signal.
async function halfBody(port: number): Promise<net.Socket> {
  const socket = net.connect(port, '127.0.0.1', () =>
    socket.write(`POST /hello HTTP/1.1\r\nHost: ${API}\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\nhalf`),
  )
  socket.on('error', () => {})
  await new Promise(resolve => socket.once('data', resolve))
  return socket
}

// Sends raw bytes to the port and resolves with everything the server sends back before it closes;
// with `end`, the client also ends its side after the bytes, as a client that gives up would.
function raw(port: number, bytes: string, { end = false } = {}): Promise<string> {
  return new Promise((resolve, reject) => {
    const socket = net.connect(port, '127.0.0.1', () => (end ? socket.end(bytes) : socket.write(bytes)))
    let text = ''
    socket.on('data', chunk => (text += chunk.toString()))
    socket.on('close', () => resolve(text))
    socket.on('error', reject)
  })
}

describe('through the proxy', () => {
  test('a request is served and recorded under its CONNECT host, and its tunnel records both requests', async () => {
    const mock = await start()
    const first = await mock.proxied(`https://${API}/hello`, { method: 'POST', body: 'body-1' })
    expect(await first.text()).toBe(`hello ${API} body-1`)
    const second = await mock.proxied(`https://${OTHER}/hello`)
    expect(await second.text()).toBe(`hello ${OTHER}`)
    const served = mock.lines.filter(line => line.startsWith('SERVED ')).map(record)
    expect(served).toMatchObject([
      { event: 'request', host: API, method: 'POST', path: '/hello', query: '', endpoint: 'hello', status: 200, outcome: 'served' },
      { event: 'request', host: OTHER, method: 'GET', path: '/hello', endpoint: 'other-hello', status: 200, outcome: 'served' },
    ])
    expect(served[0]?.tunnel).toBeNumber()
    // A generator that reports nothing is recorded as reporting nothing, not as a record without facts.
    expect(served[0]?.facts).toEqual({})
    await mock.server.stop()
    const tunnels = mock.lines.filter(line => line.startsWith('CLOSED ') && record(line).tunnel !== null).map(record)
    expect(tunnels.map(t => [t.host, t.port, t.requests]).toSorted()).toEqual([
      [API, 443, 1],
      [OTHER, 443, 1],
    ])
  })

  test('two requests to one host share one tunnel, and its record counts both', async () => {
    const mock = await start()
    for (const body of ['a', 'b']) await (await mock.proxied(`https://${API}/hello`, { method: 'POST', body })).text()
    await mock.server.stop()
    const tunnels = mock.lines.filter(line => line.startsWith('CLOSED ') && record(line).tunnel !== null).map(record)
    expect(tunnels).toMatchObject([{ event: 'connection', host: API, port: 443, requests: 2, closed_by: 'stop', outcome: 'closed' }])
    const carried = mock.lines.filter(line => line.startsWith('SERVED ')).map(line => record(line).tunnel)
    expect(carried).toEqual([tunnels[0]?.tunnel, tunnels[0]?.tunnel])
  })

  const unmatched: [string, string, string, string, Facts][] = [
    ['no endpoint', 'GET', '/nowhere', 'no endpoint', {}],
    ['an out-of-scope row', 'GET', '/refused', 'out of scope: a test reason', {}],
    ['an in-scope row with no generator', 'GET', '/ungenerated', 'no generator', {}],
    ['a generator that declines', 'GET', '/declines', 'nothing matches', { tried: ['a'] }],
  ]
  for (const [name, method, path, why, facts] of unmatched) {
    test(`${name} gets a 400 naming the request, and an UNMATCHED line`, async () => {
      const mock = await start()
      const response = await mock.proxied(`https://${API}${path}?x=1`, { method })
      expect(response.status).toBe(400)
      expect(((await response.json()) as { error: { message: string } }).error.message).toBe(
        `mock-anthropic UNMATCHED: ${method} ${API} ${path}: ${why}`,
      )
      expect(record(await mock.line('UNMATCHED '))).toEqual(expect.objectContaining({ host: API, method, path, query: '?x=1', status: 400, why, facts }))
      await mock.server.stop()
    })
  }

  test('a generator that throws gets a 500 and a FAILED line', async () => {
    const mock = await start()
    const response = await mock.proxied(`https://${API}/throws`)
    expect(response.status).toBe(500)
    expect(record(await mock.line('FAILED '))).toEqual(expect.objectContaining({ endpoint: 'throws', status: 500, why: 'generator broke', facts: {} }))
    await mock.server.stop()
  })

  test('a client that does not trust the CA leaves a FAILED tunnel line with zero requests', async () => {
    const mock = await start()
    await expect(fetch(`https://${API}/hello`, { proxy: `http://127.0.0.1:${mock.server.port}` })).rejects.toThrow()
    expect(record(await mock.line('FAILED '))).toMatchObject({
      event: 'connection',
      host: API,
      requests: 0,
      closed_by: 'client',
      why: expect.stringContaining('NODE_EXTRA_CA_CERTS'),
    })
    await mock.server.stop()
  })
})

describe('streamed bodies', () => {
  test('arrive chunk by chunk: the first is read before the generator releases the second', async () => {
    const mock = await start()
    const reader = (await mock.proxied(`https://${API}/stream`)).body!.getReader()
    expect(new TextDecoder().decode((await reader.read()).value)).toBe('one;')
    mock.controls.release()
    expect(new TextDecoder().decode((await reader.read()).value)).toBe('two;')
    expect((await reader.read()).done).toBe(true)
    expect(record(await mock.line('SERVED '))).toMatchObject({ endpoint: 'stream', outcome: 'served', facts: STREAM_FACTS })
    await mock.server.stop()
  })

  test('a body that fails partway breaks the response and is recorded FAILED', async () => {
    const mock = await start()
    // The failure reaches the client as an error, whether before or after the status arrives, never as
    // a clean end.
    await expect(mock.proxied(`https://${API}/broken`).then(response => response.text())).rejects.toThrow()
    expect(record(await mock.line('FAILED '))).toMatchObject({ endpoint: 'broken', why: 'response body failed: stream broke' })
    await mock.server.stop()
  })

  test('a client that leaves partway cancels the body and is recorded ABORTED', async () => {
    const mock = await start()
    const controller = new AbortController()
    const reader = (await mock.proxied(`https://${API}/stream`, { signal: controller.signal })).body!.getReader()
    await reader.read()
    controller.abort()
    expect(await mock.controls.cancelled).toBe('client closed before the body finished')
    expect(record(await mock.line('ABORTED '))).toMatchObject({
      endpoint: 'stream',
      status: 200,
      why: 'client closed before the body finished',
      facts: STREAM_FACTS,
    })
    await mock.server.stop()
    expect(requestLines(mock).map(line => line.split(' ')[0])).toEqual(['ABORTED'])
  })

  test('a request body Bun.serve rejects as malformed is recorded ABORTED by the mock, as its connection is', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `POST /hello HTTP/1.1\r\nHost: ${API}\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\nabc\r\n0\r\n\r\n`)
    expect(reply).toStartWith('HTTP/1.1 400 Bad Request')
    expect(record(await mock.line('ABORTED '))).toMatchObject({
      endpoint: 'hello',
      status: null,
      why: 'the mock closed the connection before the request body finished',
    })
    expect(record(await mock.line('CLOSED '))).toMatchObject({ event: 'connection', requests: 1, closed_by: 'server' })
    await mock.server.stop()
  })

  test('a client that hangs up partway through its request body is recorded ABORTED', async () => {
    const mock = await start()
    ;(await halfBody(mock.server.port)).destroy()
    expect(record(await mock.line('ABORTED '))).toMatchObject({
      endpoint: 'hello',
      status: null,
      why: 'client closed before the request body finished',
    })
    await mock.server.stop()
  })
})

describe('a generator still working', () => {
  test('when the client leaves, the request is recorded ABORTED once and its late response is cancelled', async () => {
    const mock = await start()
    const controller = new AbortController()
    const fetched = mock.proxied(`https://${API}/slow`, { signal: controller.signal }).catch(() => 'aborted')
    await mock.controls.slowBegan
    controller.abort()
    expect(await fetched).toBe('aborted')
    expect(record(await mock.line('ABORTED '))).toMatchObject({ endpoint: 'slow', status: null, why: 'client closed before the response was ready' })
    mock.controls.release()
    expect(await mock.controls.cancelled).toBe('client closed before the response was ready')
    await mock.server.stop()
    expect(requestLines(mock).map(line => line.split(' ')[0])).toEqual(['ABORTED'])
  })

  test('stop() resolves, recording the request as stopped by the mock', async () => {
    const mock = await start()
    void fetch(`http://127.0.0.1:${mock.server.port}/slow`).catch(() => {})
    await mock.controls.slowBegan
    await mock.server.stop()
    expect(requestLines(mock).map(record)).toMatchObject([
      { outcome: 'aborted', why: 'the mock stopped before the response was ready' },
    ])
    mock.controls.release()
  })
})

describe('as a plain origin', () => {
  test('a request sent straight to the port is answered as api.anthropic.com, with no tunnel', async () => {
    const mock = await start()
    const response = await fetch(`http://127.0.0.1:${mock.server.port}/hello`, { method: 'POST', body: 'plain' })
    expect(await response.text()).toBe(`hello ${API} plain`)
    expect(record(await mock.line('SERVED '))).toMatchObject({ tunnel: null, host: API, endpoint: 'hello' })
    await mock.server.stop()
  })

  test('a HEAD request gets its headers with no body and its line, and stop() still returns', async () => {
    const mock = await start()
    const plain = await fetch(`http://127.0.0.1:${mock.server.port}/nowhere`, { method: 'HEAD' })
    expect(plain.status).toBe(400)
    expect(await plain.text()).toBe('')
    const tunneled = await mock.proxied(`https://${API}/nowhere`, { method: 'HEAD' })
    expect(tunneled.status).toBe(400)
    await mock.server.stop()
    expect(requestLines(mock).map(record)).toMatchObject([
      { tunnel: null, method: 'HEAD', path: '/nowhere', outcome: 'unmatched', why: 'no endpoint' },
      { tunnel: 0, method: 'HEAD', path: '/nowhere', outcome: 'unmatched', why: 'no endpoint' },
    ])
  })

  test('an absolute-form target naming api.anthropic.com is answered as one in origin form', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `GET http://${API}/nowhere?x=1 HTTP/1.1\r\nHost: ${API}\r\nConnection: close\r\n\r\n`)
    expect(reply).toContain(`mock-anthropic UNMATCHED: GET ${API} /nowhere: no endpoint`)
    expect(record(await mock.line('UNMATCHED '))).toMatchObject({ tunnel: null, host: API, path: '/nowhere', query: '?x=1' })
    await mock.server.stop()
  })

  test('an absolute-form target naming another host, as an HTTP_PROXY client sends, is refused with a 403 naming it', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `GET http://${OTHER}/hello HTTP/1.1\r\nHost: ${OTHER}\r\n\r\n`)
    const why = `not ${API}, the only host the plain port answers; others are reached through CONNECT`
    expect(reply).toStartWith('HTTP/1.1 403 Forbidden\r\n')
    expect(reply).toEndWith(`mock-anthropic REFUSED: http://${OTHER}/hello: ${why}\n`)
    expect(record(await mock.line('REFUSED '))).toEqual({ event: 'connection', tunnel: null, host: OTHER, port: null, outcome: 'refused', why })
    await mock.server.stop()
    expect(mock.lines).toHaveLength(1)
  })

  test("a response the mock cannot pass on is a 500 in the API's error envelope, and a FAILED line", async () => {
    const mock = await start()
    const response = await fetch(`http://127.0.0.1:${mock.server.port}/locked`)
    expect(response.status).toBe(500)
    expect(response.headers.get('x-should-retry')).toBe('false')
    const { type, error } = (await response.json()) as { type: string; error: { type: string; message: string } }
    expect({ type, errorType: error.type }).toEqual({ type: 'error', errorType: 'api_error' })
    expect(error.message).toStartWith(`mock-anthropic FAILED: GET ${API} /locked: the mock failed: `)
    expect(record(await mock.line('FAILED '))).toMatchObject({ event: 'request', endpoint: 'locked', status: 500 })
    await mock.server.stop()
  })

  test('a target that looks like an authority (//v1/messages) stays a path under api.anthropic.com', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `POST //v1/messages HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\nConnection: close\r\n\r\n`)
    expect(reply).toContain(`mock-anthropic UNMATCHED: POST ${API} //v1/messages: no endpoint`)
    expect(record(await mock.line('UNMATCHED '))).toMatchObject({ host: API, path: '//v1/messages' })
    await mock.server.stop()
  })
})

describe('a plain connection that is not served', () => {
  test('TLS sent straight to the port is refused at once, naming the handshake, and the client fails promptly', async () => {
    const mock = await start()
    await expect(fetch(`https://127.0.0.1:${mock.server.port}/hello`, { tls: { ca: mock.ca } })).rejects.toThrow()
    expect(record(await mock.line('REFUSED '))).toEqual({
      event: 'connection',
      tunnel: null,
      host: API,
      port: null,
      outcome: 'refused',
      why: 'not an HTTP request: it opens with byte 0x16, a TLS handshake: the port speaks plain HTTP, and HTTPS only through CONNECT',
    })
    await mock.server.stop()
    expect(mock.lines).toHaveLength(1)
  })

  for (const [name, line, why] of [
    ['a malformed request line', 'GARBAGE\r\n', 'malformed request line: "GARBAGE\\r"'],
    ['an HTTP version the mock does not speak', 'GET /hello HTTP/9.9\r\n', 'malformed request line: "GET /hello HTTP/9.9\\r"'],
    ['a request line ended by a bare LF', 'GET /hello HTTP/1.1\n', 'malformed request line: "GET /hello HTTP/1.1"'],
    ['a method in lower case', 'get /hello HTTP/1.1\r\n', 'malformed request line: "get /hello HTTP/1.1\\r"'],
  ] as const) {
    test(`${name} is refused at once with a 400 naming it`, async () => {
      const mock = await start()
      const reply = await raw(mock.server.port, `${line}Host: ${API}\r\n\r\n`)
      expect(reply).toStartWith('HTTP/1.1 400 Bad Request\r\n')
      expect(reply).toEndWith(`mock-anthropic REFUSED: ${why}\n`)
      expect(record(await mock.line('REFUSED '))).toMatchObject({ event: 'connection', tunnel: null, why })
      await mock.server.stop()
    })
  }

  test('a request line that never ends is refused once it passes the limit', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `GET /${'a'.repeat(9000)}`)
    expect(reply).toEndWith('mock-anthropic REFUSED: request line longer than 8192 bytes\n')
    await mock.server.stop()
  })

  test('a request Bun.serve rejects before the mock sees it (oversized headers) leaves a FAILED connection line', async () => {
    const mock = await start()
    await raw(mock.server.port, `GET /hello HTTP/1.1\r\nHost: ${API}\r\nX-Big: ${'a'.repeat(20000)}\r\n\r\n`, { end: true })
    expect(record(await mock.line('FAILED '))).toMatchObject({
      event: 'connection',
      tunnel: null,
      requests: 0,
      why: 'closed before any request reached the mock: Bun.serve answers a malformed request or oversized headers itself, before the mock sees it, and a client that closes partway through its request head, or ends its side (a half-close) before the mock has read the request, leaves this way too',
    })
    await mock.server.stop()
  })

  test('a client that connects and leaves having sent nothing is recorded CLOSED, with zero requests', async () => {
    const mock = await start()
    await new Promise<void>(resolve => net.connect(mock.server.port, '127.0.0.1', function (this: net.Socket) {
      this.end()
      this.once('close', () => resolve())
    }))
    expect(record(await mock.line('CLOSED '))).toMatchObject({ event: 'connection', tunnel: null, requests: 0, closed_by: 'client' })
    await mock.server.stop()
  })

  test('a client that leaves partway through its first line is recorded FAILED', async () => {
    const mock = await start()
    await raw(mock.server.port, 'GET /hel', { end: true })
    expect(record(await mock.line('FAILED '))).toMatchObject({ requests: 0, why: 'closed before finishing its request line' })
    await mock.server.stop()
  })
})

describe('CONNECT refusals', () => {
  for (const target of [`user@${API}:443`, `${API}/x:443`, `${API}\\x:443`, `api%2eanthropic.com:443`]) {
    test(`a target with more than a host and port (${target}) is refused as malformed`, async () => {
      const mock = await start()
      const reply = await raw(mock.server.port, `CONNECT ${target} HTTP/1.1\r\nHost: ${API}:443\r\n\r\n`)
      expect(reply).toEndWith(`mock-anthropic REFUSED: CONNECT ${target}: malformed CONNECT target\n`)
      expect(record(await mock.line('REFUSED '))).toMatchObject({ host: target, port: null, why: 'malformed CONNECT target' })
      await mock.server.stop()
    })
  }

  test('a CONNECT head that never ends is refused once it passes the limit', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `CONNECT ${API}:443 HTTP/1.1\r\nX: ${'a'.repeat(70000)}`)
    expect(reply).toEndWith('mock-anthropic REFUSED: CONNECT head longer than 65536 bytes\n')
    await mock.server.stop()
  })

  test("a broken body's connection is recorded as closed by the mock, not the client", async () => {
    const mock = await start()
    await expect(fetch(`http://127.0.0.1:${mock.server.port}/broken`).then(response => response.text())).rejects.toThrow()
    expect(record(await mock.line('CLOSED '))).toMatchObject({ event: 'connection', tunnel: null, closed_by: 'server' })
    await mock.server.stop()
  })

  test('a Host header that is not a valid authority still gets its request recorded', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, 'GET /nowhere HTTP/1.1\r\nHost: a b\r\nConnection: close\r\n\r\n')
    expect(reply).toContain(`mock-anthropic UNMATCHED: GET ${API} /nowhere: no endpoint`)
    expect(record(await mock.line('UNMATCHED '))).toMatchObject({ host: API, path: '/nowhere' })
    await mock.server.stop()
  })

  test('a host the table does not know gets a 403 naming it, and a REFUSED line', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, 'CONNECT unknown.example:443 HTTP/1.1\r\nHost: unknown.example:443\r\n\r\n')
    expect(reply).toStartWith('HTTP/1.1 403 Forbidden\r\n')
    expect(reply).toEndWith('mock-anthropic REFUSED: CONNECT unknown.example:443: not a host the mock answers\n')
    expect(record(await mock.line('REFUSED '))).toEqual({
      event: 'connection',
      tunnel: null,
      host: 'unknown.example',
      port: 443,
      outcome: 'refused',
      why: 'not a host the mock answers',
    })
    await mock.server.stop()
  })

  test("a table host on a port other than 443 is refused, and its record keeps the port, though 80 is http's default", async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `CONNECT ${API}:80 HTTP/1.1\r\nHost: ${API}:80\r\n\r\n`)
    expect(reply).toStartWith('HTTP/1.1 403 Forbidden\r\n')
    expect(reply).toEndWith(`mock-anthropic REFUSED: CONNECT ${API}:80: not port 443, the only port the mock answers\n`)
    expect(record(await mock.line('REFUSED '))).toEqual({
      event: 'connection',
      tunnel: null,
      host: API,
      port: 80,
      outcome: 'refused',
      why: 'not port 443, the only port the mock answers',
    })
    await mock.server.stop()
  })

  test('a target with no port is refused as malformed', async () => {
    const mock = await start()
    const reply = await raw(mock.server.port, `CONNECT ${API} HTTP/1.1\r\nHost: ${API}\r\n\r\n`)
    expect(reply).toEndWith(`mock-anthropic REFUSED: CONNECT ${API}: malformed CONNECT target\n`)
    expect(record(await mock.line('REFUSED '))).toMatchObject({ host: API, port: null })
    await mock.server.stop()
  })
})

function requestLines(mock: Running): string[] {
  return mock.lines.filter(line => record(line).event === 'request')
}

describe('stop', () => {
  test('resolves when called right after a CONNECT is written, before its 200, and nothing goes live after', async () => {
    const mock = await start()
    const socket = net.connect(mock.server.port, '127.0.0.1', () =>
      socket.write(`CONNECT ${API}:443 HTTP/1.1\r\nHost: ${API}:443\r\n\r\n`),
    )
    socket.on('error', () => {})
    const socketClosed = new Promise(resolve => socket.once('close', resolve))
    await new Promise(resolve => socket.once('connect', resolve))
    await mock.server.stop()
    const after = mock.lines.length
    await socketClosed
    expect(mock.lines.length).toBe(after)
  })

  test('resolves while a request body is still arriving, having recorded it as stopped by the mock', async () => {
    const mock = await start()
    await halfBody(mock.server.port)
    await mock.server.stop()
    expect(mock.lines.map(record)).toMatchObject([
      { event: 'request', outcome: 'aborted', why: 'the mock stopped before the request body finished' },
      { event: 'connection', tunnel: null, requests: 1, closed_by: 'stop', outcome: 'closed' },
    ])
  })

  test('resolves while a CONNECT is held open with no TLS, and records the tunnel as closed by the mock', async () => {
    const mock = await start()
    const socket = net.connect(mock.server.port, '127.0.0.1')
    socket.on('error', () => {})
    socket.write(`CONNECT ${API}:443 HTTP/1.1\r\nHost: ${API}:443\r\n\r\n`)
    await new Promise(resolve => socket.once('data', resolve))
    await mock.server.stop()
    expect(mock.lines.map(record)).toMatchObject([
      { event: 'connection', tunnel: 0, host: API, requests: 0, closed_by: 'stop', outcome: 'closed' },
    ])
  })

  test('resolves during an unfinished plain-origin stream, having recorded it ABORTED', async () => {
    const mock = await start()
    const reader = (await fetch(`http://127.0.0.1:${mock.server.port}/stream`)).body!.getReader()
    await reader.read()
    await mock.server.stop()
    expect(mock.lines.map(record)).toMatchObject([
      { event: 'request', endpoint: 'stream', outcome: 'aborted', why: 'the mock stopped before the body finished' },
      { event: 'connection', tunnel: null, requests: 1, closed_by: 'stop', outcome: 'closed' },
    ])
    await expect(reader.read()).rejects.toThrow()
  })
})

// The directory of a server's inner unix sockets, found as the one that appeared while it started.
async function startWithSocketDir(): Promise<{ mock: Running; socketDir: string }> {
  const socketDirs = async () => (await readdir(tmpdir())).filter(name => name.startsWith('mock-anthropic-') && !name.startsWith('mock-anthropic-server-'))
  const before = new Set(await socketDirs())
  const mock = await start()
  const [socketDir] = (await socketDirs()).filter(name => !before.has(name))
  return { mock, socketDir: join(tmpdir(), socketDir!) }
}

describe('a connection whose inner side breaks', () => {
  test('an inner server that cannot start ends the connection at once, and its FAILED line says why', async () => {
    const { mock, socketDir } = await startWithSocketDir()
    await chmod(socketDir, 0o500)
    try {
      await expect(fetch(`http://127.0.0.1:${mock.server.port}/hello`, { method: 'POST' })).rejects.toThrow()
      expect(record(await mock.line('FAILED '))).toMatchObject({
        event: 'connection',
        requests: 0,
        closed_by: 'server',
        why: expect.stringMatching(/^inner server could not start: EACCES/),
      })
    } finally {
      await chmod(socketDir, 0o700)
    }
    await mock.server.stop()
  })

  test('an inner side the front cannot connect to ends the connection, and its FAILED line says why', async () => {
    const mock = await start()
    // A socket created under a full umask cannot be connected to.
    const umask = process.umask(0o777)
    try {
      await expect(fetch(`http://127.0.0.1:${mock.server.port}/hello`, { method: 'POST' })).rejects.toThrow()
    } finally {
      process.umask(umask)
    }
    expect(record(await mock.line('FAILED '))).toMatchObject({
      event: 'connection',
      requests: 0,
      closed_by: 'server',
      why: expect.stringMatching(/^inner side failed: /),
    })
    await mock.server.stop()
  })
})

describe('starting', () => {
  test('on a port already taken, the start fails and leaves no socket directory behind', async () => {
    const taken = net.createServer()
    await new Promise<void>(resolve => taken.listen(0, '127.0.0.1', () => resolve()))
    const { port } = taken.address() as net.AddressInfo
    const socketDirs = async () => (await readdir(tmpdir())).filter(name => name.startsWith('mock-anthropic-') && !name.startsWith('mock-anthropic-server-'))
    const before = await socketDirs()
    await expect(startServer({ port, table: TABLE, generators: generators().generators, certsDir, emit: () => {} })).rejects.toThrow()
    expect(await socketDirs()).toEqual(before)
    await new Promise(resolve => taken.close(resolve))
  })
})

describe("the consumer's sink", () => {
  // reportError fails whatever test runs it, so the mock runs in a process of its own, whose exit code
  // and output are what is checked.
  test('one that throws is reported loudly, and the request, its connection and stop() all still settle', async () => {
    const script = `
      import { startServer } from ${JSON.stringify(join(import.meta.dir, 'server.ts'))}
      import { API, buildEndpointTable } from ${JSON.stringify(join(import.meta.dir, 'endpoints.ts'))}
      const table = buildEndpointTable([{ id: 'hello', host: API, method: 'GET', path: '/hello', scope: 'in-scope' }])
      const lines = []
      const emit = event => { if (lines.push(event.outcome) === 1) throw new Error('the sink broke') }
      const server = await startServer({ port: 0, table, generators: { hello: () => ({ kind: 'response', response: new Response('hi') }) }, certsDir: ${JSON.stringify(certsDir)}, emit })
      const response = await fetch('http://127.0.0.1:' + server.port + '/hello')
      console.log(response.status, await response.text())
      await server.stop()
      console.log('stopped', lines.join(','))
    `
    const child = Bun.spawn([process.execPath, '-e', script], { stdout: 'pipe', stderr: 'pipe' })
    const [stdout, stderr, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
    expect(stdout).toBe('200 hi\nstopped served,closed\n')
    expect(stderr).toContain('the sink broke')
    expect(code).toBe(1)
  })
})
