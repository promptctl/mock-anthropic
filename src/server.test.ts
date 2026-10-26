import { afterAll, describe, expect, test } from 'bun:test'
import { mkdtemp, rm } from 'node:fs/promises'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Generators } from './dispatch.ts'
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
] as const

const TABLE = buildEndpointTable(ROWS)

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
  const respond = (body: string | ReadableStream<Uint8Array>) => ({ kind: 'response', response: new Response(body) }) as const
  return {
    controls: { release, cancelled, slowBegan },
    generators: {
      hello: (_, request) => respond(`hello ${request.url.hostname} ${request.body}`),
      'other-hello': (_, request) => respond(`hello ${request.url.hostname}`),
      declines: () => ({ kind: 'declined', why: 'nothing matches' }),
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
        ),
      // Answers only once released, with a body that records its cancel.
      slow: async () => {
        began()
        await released
        return respond(new ReadableStream({ cancel: reason => cancel(String(reason)) }))
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

  const unmatched: [string, string, string, string][] = [
    ['no endpoint', 'GET', '/nowhere', 'no endpoint'],
    ['an out-of-scope row', 'GET', '/refused', 'out of scope: a test reason'],
    ['an in-scope row with no generator', 'GET', '/ungenerated', 'no generator'],
    ['a generator that declines', 'GET', '/declines', 'nothing matches'],
  ]
  for (const [name, method, path, why] of unmatched) {
    test(`${name} gets a 400 naming the request, and an UNMATCHED line`, async () => {
      const mock = await start()
      const response = await mock.proxied(`https://${API}${path}?x=1`, { method })
      expect(response.status).toBe(400)
      expect(((await response.json()) as { error: { message: string } }).error.message).toBe(
        `mock-anthropic UNMATCHED: ${method} ${API} ${path}: ${why}`,
      )
      expect(record(await mock.line('UNMATCHED '))).toMatchObject({ host: API, method, path, query: '?x=1', status: 400, why })
      await mock.server.stop()
    })
  }

  test('a generator that throws gets a 500 and a FAILED line', async () => {
    const mock = await start()
    const response = await mock.proxied(`https://${API}/throws`)
    expect(response.status).toBe(500)
    expect(record(await mock.line('FAILED '))).toMatchObject({ endpoint: 'throws', status: 500, why: 'generator broke' })
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
    expect(record(await mock.line('SERVED '))).toMatchObject({ endpoint: 'stream', outcome: 'served' })
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
    expect(record(await mock.line('ABORTED '))).toMatchObject({ endpoint: 'stream', status: 200, why: 'client closed before the body finished' })
    await mock.server.stop()
    expect(requestLines(mock).map(line => line.split(' ')[0])).toEqual(['ABORTED'])
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
    ['a malformed request line', 'GARBAGE', 'malformed request line: "GARBAGE"'],
    ['an HTTP version the mock does not speak', 'GET /hello HTTP/9.9', 'malformed request line: "GET /hello HTTP/9.9"'],
  ] as const) {
    test(`${name} is refused at once with a 400 naming it`, async () => {
      const mock = await start()
      const reply = await raw(mock.server.port, `${line}\r\nHost: ${API}\r\n\r\n`)
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
      why: 'closed before any request reached the mock: Bun.serve answers a malformed request or oversized headers itself, before the mock sees it',
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

  test("a table host on port 80 is tunneled, though 80 is http's default port", async () => {
    const mock = await start()
    const socket = net.connect(mock.server.port, '127.0.0.1', () => socket.write(`CONNECT ${API}:80 HTTP/1.1\r\nHost: ${API}:80\r\n\r\n`))
    socket.on('error', () => {})
    const reply = await new Promise<string>(resolve => socket.once('data', chunk => resolve(chunk.toString())))
    expect(reply).toStartWith('HTTP/1.1 200 Connection Established')
    socket.destroy()
    expect(record(await mock.line('FAILED '))).toMatchObject({ event: 'connection', host: API, port: 80, requests: 0 })
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
