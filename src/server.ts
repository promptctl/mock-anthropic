import { mkdtemp, rm } from 'node:fs/promises'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { type Certificates, ensureCertificates } from './certificates.ts'
import { dispatch, errorResponse, type Generators, type Verdict } from './dispatch.ts'
import { API, type EndpointRow, type EndpointTable, resolveEndpoint, tableHosts } from './endpoints.ts'

// PROJECT.md "How a program reaches it" and "Failure behavior": one listener on 127.0.0.1 that is
// both an HTTPS forward proxy for the table's hosts and a plain origin for api.anthropic.com. It moves
// bytes between sockets and `dispatch`, and it is the one place a record of each unit of work is made.

// [LAW:nothing-unseen] One record per unit of work: each request and each connection, the plain ones,
// the tunnels and the refused ones alike. A request's `tunnel` is the id of the tunnel that carried it, or null for one sent straight to the
// port. `status` is null when the client left before a response existed.
export type RequestEvent = {
  readonly event: 'request'
  readonly tunnel: number | null
  readonly host: string
  readonly method: string
  readonly path: string
  readonly query: string
  readonly endpoint: string | null
  readonly status: number | null
  readonly duration_ms: number
} & (Verdict | { readonly outcome: 'aborted'; readonly why: string })

// A connection's `host` is the authority its requests are addressed to, and `port` the port its CONNECT
// named: null for a plain connection, and for a CONNECT whose target had none. A connection that was
// served says how many requests reached the mock and who closed it; one that closed having answered
// none, unless `stop()` closed it, failed.
export type ConnectionEvent = {
  readonly event: 'connection'
  readonly tunnel: number | null
  readonly host: string
  readonly port: number | null
} & (
  | { readonly outcome: 'refused'; readonly why: string }
  | ({ readonly requests: number; readonly duration_ms: number; readonly closed_by: Closer | 'server' } & (
      | { readonly outcome: 'closed' }
      | { readonly outcome: 'failed'; readonly why: string }
    ))
)

// Who ended a unit: the client, or the mock's own `stop()`. A connection can also be ended by its
// inner server, as Bun.serve does when it rejects a malformed request.
export type Closer = 'client' | 'stop'

export type ServerEvent = RequestEvent | ConnectionEvent

// The line `serve` writes to stderr for an event: its outcome in capitals, so a consumer can fail on
// any line that starts with UNMATCHED, REFUSED or FAILED, then the whole record as JSON.
export function eventLine(event: ServerEvent): string {
  return `${event.outcome.toUpperCase()} ${JSON.stringify(event)}`
}

export type ServerOptions<R extends EndpointRow> = {
  readonly port: number
  readonly table: EndpointTable<R>
  readonly generators: Generators<R>
  readonly certsDir: string
  readonly emit: (event: ServerEvent) => void
}

export type MockServer = { readonly port: number; readonly certificates: Certificates; stop(): Promise<void> }

type Tls = { readonly key: string; readonly cert: string }

// What a front connection is served as: the authority every request on it is addressed to, the
// tunnel it is (null for the plain origin) and the port its CONNECT named, the TLS it speaks, the
// bytes the client gets first, the client's bytes the front has already read, and why a connection
// that answered no request failed.
type Kind = {
  readonly authority: string
  readonly tunnel: number | null
  readonly port: number | null
  readonly tls: Tls | null
  readonly greeting: string
  readonly replay: Buffer
  readonly unanswered: string
}

const LOOPBACK = '127.0.0.1'
const ESTABLISHED = 'HTTP/1.1 200 Connection Established\r\n\r\n'
// Every host in the table speaks HTTPS on 443 and nothing else, and a tunnel is always TLS.
const HTTPS_PORT = 443
// Bun.serve answers a malformed request line or oversized headers itself, before the mock sees the
// request, and Bun gives no sign that a tunnel's TLS handshake finished, so a tunnel's zero requests
// name every cause beside the likeliest one.
const TUNNEL_UNANSWERED =
  "closed before any request reached the mock: a client that does not trust the mock's CA (NODE_EXTRA_CA_CERTS) closes its tunnel this way, and so do a request Bun.serve answers itself as malformed or with oversized headers, and a client that opens a tunnel and never uses it"
const PLAIN_UNANSWERED = 'closed before any request reached the mock: Bun.serve answers a malformed request or oversized headers itself, before the mock sees it'
const UNFINISHED_LINE = 'closed before finishing its request line'
// An HTTP/1 request line: a method (a token; every method Claude Code sends is capitals), a target, and
// the version, ended by CRLF. Bun.serve, given a line that is not one, neither answers nor closes, so
// the front checks the first line itself and refuses one that is not, and one that grows past MAX_LINE
// without ending. A line ended by a bare LF, which RFC 9112 2.2 lets a server accept or refuse, is
// refused, so a client that sends one hears why instead of waiting on a CRLF that never comes.
const REQUEST_START = /^[A-Z]/
const REQUEST_LINE = /^([A-Z]+) (\S+) HTTP\/1\.[01]\r$/
const MAX_LINE = 8192
// Why a request ended early: who ended it, and before what.
const BODY_CUT_OFF = 'the request body finished'
const NOT_READY = 'the response was ready'
const BODY_UNFINISHED = 'the body finished'
function endedEarly(closer: Closer, before: string): string {
  return `${closer === 'client' ? 'client closed' : 'the mock stopped'} before ${before}`
}
const MAX_HEAD = 65536

export async function startServer<R extends EndpointRow>(options: ServerOptions<R>): Promise<MockServer> {
  const { table, generators, emit } = options
  const hosts = tableHosts(table)
  // [LAW:one-source-of-truth] The leaf covers exactly the hosts this server accepts CONNECT for: both
  // are read from the one table it was given.
  const certificates = await ensureCertificates(options.certsDir, hosts)
  const tls: Tls = { key: await Bun.file(certificates.leaf.keyPath).text(), cert: await Bun.file(certificates.leaf.certPath).text() }
  const socketDir = await mkdtemp(join(tmpdir(), 'mock-anthropic-'))

  // [LAW:no-ambient-temporal-coupling] This instance owns every socket it accepts or opens and every
  // unit of work in flight, each added the moment it starts, so `stop()` ends them itself instead of
  // waiting on how far each client got.
  const sockets = new Set<net.Socket>()
  const inFlight = new Set<Promise<void>>()
  let nextTunnel = 0
  let nextSocket = 0
  // Set once, by `stop()`, before it closes anything, so a unit it ends is recorded as ended by it.
  let stopping = false
  const closer = (): Closer => (stopping ? 'stop' : 'client')

  const own = (socket: net.Socket) => {
    sockets.add(socket)
    socket.once('close', () => sockets.delete(socket))
  }
  const track = (work: Promise<void>) => {
    inFlight.add(work)
    void work.finally(() => inFlight.delete(work))
  }

  // [LAW:dataflow-not-control-flow] A plain connection and a tunnel are one shape; they differ only in
  // the values of their Kind.
  const serve = (client: net.Socket, kind: Kind) =>
    connection(client, kind, join(socketDir, `${nextSocket++}.sock`), own, closer, (request, carrier) => {
      const { response, recorded } = serveRequest(table, generators, emit, kind, request, carrier, closer)
      track(recorded)
      return response
    })

  // One record per served connection, when it ends.
  const served = (client: net.Socket, kind: Kind) =>
    serve(client, kind).then(({ duration_ms, requests, error, closed_by }) => {
      const base = { event: 'connection', tunnel: kind.tunnel, host: kind.authority, port: kind.port, requests, duration_ms, closed_by } as const
      const why = error ?? (requests === 0 && closed_by !== 'stop' ? kind.unanswered : null)
      emit(why === null ? { ...base, outcome: 'closed' } : { ...base, outcome: 'failed', why })
    })

  // A connection addressed to a host or port the mock does not answer gets a 403 naming it, and its record.
  const forbid = (client: net.Socket, host: string, port: number | null, what: string, why: string) => {
    refuse(client, '403 Forbidden', `${what}: ${why}`)
    emit({ event: 'connection', tunnel: null, host, port, outcome: 'refused', why })
  }

  const tunnel = (client: net.Socket, target: string, rest: Buffer) => {
    const parsed = connectTarget(target)
    const why =
      parsed.port === null
        ? 'malformed CONNECT target'
        : !hosts.includes(parsed.host)
          ? 'not a host the mock answers'
          : parsed.port !== HTTPS_PORT
            ? `not port ${HTTPS_PORT}, the only port the mock answers`
            : null
    if (why !== null) return forbid(client, parsed.host, parsed.port, `CONNECT ${target}`, why)
    return served(client, {
      authority: parsed.host,
      tunnel: nextTunnel++,
      port: parsed.port,
      tls,
      greeting: ESTABLISHED,
      replay: rest,
      unanswered: TUNNEL_UNANSWERED,
    })
  }

  // RFC 9112 3.2.2: a request whose target is in absolute form is addressed to the host it names. The
  // plain port is the origin for api.anthropic.com alone, so an http:// URL for any other host, which an
  // HTTP_PROXY client sends this way, is refused as a CONNECT to an unknown host is. The front reads only
  // a connection's first request line, so a connection is judged by its first request.
  const plain = (client: net.Socket, target: string, read: Buffer) => {
    const host = target.startsWith('/') ? API : (URL.parse(target)?.hostname ?? target)
    if (host !== API) return forbid(client, host, null, target, `not ${API}, the only host the plain port answers; others are reached through CONNECT`)
    return served(client, { authority: API, tunnel: null, port: null, tls: null, greeting: '', replay: read, unanswered: PLAIN_UNANSWERED })
  }

  const front = net.createServer(client => {
    const started = performance.now()
    own(client)
    client.on('error', () => client.destroy())
    const unserved = { event: 'connection', tunnel: null, host: API, port: null } as const
    track(
      opening(client).then(first => {
        switch (first.kind) {
          case 'connect':
            return tunnel(client, first.target, first.rest)
          case 'plain':
            return plain(client, first.target, first.read)
          case 'refused':
            refuse(client, '400 Bad Request', first.why)
            return emit({ ...unserved, outcome: 'refused', why: first.why })
          case 'closed': {
            // A client that connects and leaves having sent nothing asked for nothing; one that left
            // partway through its first line did not get what it was sending, unless `stop()` cut it.
            const base = { ...unserved, requests: 0, duration_ms: elapsed(started), closed_by: closer() } as const
            return emit(
              first.read === 0 || base.closed_by === 'stop' ? { ...base, outcome: 'closed' } : { ...base, outcome: 'failed', why: UNFINISHED_LINE },
            )
          }
        }
      }),
    )
  })

  // A port that cannot be listened on fails the start, and leaves nothing behind.
  await new Promise<void>((resolve, reject) => {
    front.once('error', reject)
    front.listen(options.port, LOOPBACK, () => resolve())
  }).catch(async (error: unknown) => {
    await rm(socketDir, { recursive: true, force: true })
    throw error
  })
  const { port } = front.address() as net.AddressInfo

  return {
    port,
    certificates,
    stop: async () => {
      stopping = true
      const closed = new Promise<void>(resolve => front.close(() => resolve()))
      for (const socket of sockets) socket.destroy()
      // A unit settles once its sockets are gone, after emitting its record, and the units still
      // running can only add ones that settle the same way, so the set drains.
      while (inFlight.size > 0) await Promise.all(inFlight)
      await closed
      await rm(socketDir, { recursive: true, force: true })
    },
  }
}

// What a connection opens with, read only as far as the front needs to choose. A CONNECT is read to
// the end of its head, so the bytes after it, the start of the client's TLS, are told apart from it.
// Anything else is the plain origin, with every byte read so far, which its inner server parses.
// Bytes that cannot open a request line are refused at once, rather than waited on for a line end a
// TLS handshake may never contain, and so is a first line that is not a request line. A client that closes first says how many bytes it had sent.
type Opening =
  | { readonly kind: 'connect'; readonly target: string; readonly rest: Buffer }
  | { readonly kind: 'plain'; readonly target: string; readonly read: Buffer }
  | { readonly kind: 'refused'; readonly why: string }
  | { readonly kind: 'closed'; readonly read: number }

function opening(client: net.Socket): Promise<Opening> {
  return new Promise(resolve => {
    let read = Buffer.alloc(0)
    const settle = (first: Opening) => {
      client.off('data', onData)
      client.off('close', onClose)
      client.pause()
      resolve(first)
    }
    const onClose = () => settle({ kind: 'closed', read: read.length })
    const onData = (chunk: Buffer) => {
      read = Buffer.concat([read, chunk])
      if (!REQUEST_START.test(read.toString('latin1', 0, 1))) return settle({ kind: 'refused', why: notARequest(read[0] as number) })
      const lineEnd = read.indexOf('\n')
      if (lineEnd === -1) {
        if (read.length > MAX_LINE) settle({ kind: 'refused', why: `request line longer than ${MAX_LINE} bytes` })
        return
      }
      const line = read.subarray(0, lineEnd).toString('latin1')
      const [, method, target] = REQUEST_LINE.exec(line) ?? []
      if (target === undefined) return settle({ kind: 'refused', why: `malformed request line: ${JSON.stringify(line.slice(0, 200))}` })
      if (method !== 'CONNECT') return settle({ kind: 'plain', target, read })
      const headEnd = read.indexOf('\r\n\r\n')
      if (headEnd !== -1) settle({ kind: 'connect', target, rest: read.subarray(headEnd + 4) })
      else if (read.length > MAX_HEAD) settle({ kind: 'refused', why: `CONNECT head longer than ${MAX_HEAD} bytes` })
    }
    client.on('data', onData)
    client.once('close', onClose)
  })
}

function notARequest(byte: number): string {
  const hex = `0x${byte.toString(16).padStart(2, '0')}`
  // 0x16 is a TLS record's handshake type: an https:// URL pointed straight at the port.
  const tls = byte === 0x16 ? ', a TLS handshake: the port speaks plain HTTP, and HTTPS only through CONNECT' : ''
  return `not an HTTP request: it opens with byte ${hex}${tls}`
}

// The host and port of a CONNECT's authority-form target. One that does not parse, or names no port,
// keeps its raw text as the host and has a null port, so its refusal can still name it.
function connectTarget(text: string): { host: string; port: number } | { host: string; port: null } {
  // The port is read from the text, not from a URL, which drops a port equal to its scheme's default.
  const [, authority, port] = /^(.+):(\d{1,5})$/.exec(text) ?? []
  const host = authority === undefined ? null : URL.parse(`http://${authority}/`)?.hostname
  return host === null || host === undefined || Number(port) > 65535 ? { host: text, port: null } : { host, port: Number(port) }
}

function refuse(client: net.Socket, status: string, what: string): void {
  const body = `mock-anthropic REFUSED: ${what}\n`
  // The front paused the socket to read its opening, so its readable side never ends on its own: once
  // the refusal is flushed, the socket is destroyed, which closes it and drops it from the owned set.
  client.end(
    `HTTP/1.1 ${status}\r\ncontent-type: text/plain\r\ncontent-length: ${Buffer.byteLength(body)}\r\nconnection: close\r\n\r\n${body}`,
    () => client.destroy(),
  )
}

// [LAW:locality-or-seam] exception: Bun 1.3.14 cannot run server-side TLS on a socket it has already
// accepted (tls.TLSSocket with isServer, and tls/https Server.emit('connection'), stall after the
// ClientHello), and its node:http never reports a client's disconnect once the request body has been
// read. So each front connection is a socket pair into a Bun.serve of its own, on a unix socket, and
// the one TCP listener stays the only one on 127.0.0.1. The connection ends when either side closes,
// and resolves with how long it lived, how many requests it carried, and the inner side's error, if
// any; a client's reset is its own choice and not an error of the mock's.
async function connection(
  client: net.Socket,
  kind: Kind,
  path: string,
  own: (socket: net.Socket) => void,
  closer: () => Closer,
  handle: (request: Request, connection: Carrier) => Promise<Response>,
): Promise<{ duration_ms: number; requests: number; error: string | null; closed_by: Closer | 'server' }> {
  const started = performance.now()
  const clientClosed = closed(client)
  // The requests waiting on this connection's end, each removed once its record is written, so a
  // long-lived connection holds nothing of the requests it has finished.
  const waiting = new Set<() => void>()
  let over = false
  const markEnded = () => {
    over = true
    for (const abandon of waiting) abandon()
  }
  const onEnd = (abandon: () => void) => {
    if (over) abandon()
    waiting.add(abandon)
    return () => waiting.delete(abandon)
  }
  let requests = 0
  let error: string | null = null
  let killed = false
  const kill = () => {
    killed = true
    client.destroy()
  }
  const inner = startInner(() => Bun.serve({
    unix: path,
    ...(kind.tls === null ? {} : { tls: kind.tls }),
    // Bun.serve closes a connection idle for 10 s by default, which would cut a slow stream or a
    // kept-alive client; a connection here lives as long as its client keeps it.
    // [LAW:types-are-the-program] exception: Bun 1.3.14's types declare idleTimeout only for
    // hostname/port servers, but a unix-socket server honors it (0 keeps an idle connection open, the
    // default closes it after about 10 s), so the options are cast past the type that omits it.
    idleTimeout: 0,
    fetch: (request: Request) => {
      requests += 1
      return handle(request, { kill, onEnd })
    },
  } as unknown as Parameters<typeof Bun.serve>[0]))
  // [LAW:no-silent-failure] An inner server that cannot start (out of file descriptors, say) ends the
  // connection at once, and the record says why.
  if (inner instanceof Error) {
    client.destroy()
    await clientClosed
    markEnded()
    return { duration_ms: elapsed(started), requests, error: `inner server could not start: ${inner.message}`, closed_by: 'server' }
  }
  const upstream = net.connect(path)
  own(upstream)
  upstream.on('error', (e: Error) => {
    error ??= `inner side failed: ${e.message}`
  })
  const upstreamClosed = closed(upstream)
  const ready = await Promise.race([
    new Promise<'open'>(resolve => upstream.once('connect', () => resolve('open'))),
    upstreamClosed,
    clientClosed,
  ])
  if (ready === 'open') {
    client.write(kind.greeting)
    upstream.write(kind.replay)
    client.pipe(upstream)
    upstream.pipe(client)
  }
  // Whichever side closed first ended the connection, unless `stop()` was closing everything or the
  // mock broke it after a failed body.
  const first = await Promise.race([clientClosed.then(() => 'client' as const), upstreamClosed.then(() => 'server' as const)])
  const closed_by = closer() === 'stop' ? 'stop' : killed ? 'server' : first
  client.destroy()
  upstream.destroy()
  await Promise.all([clientClosed, upstreamClosed])
  // Every request this connection carried is over now: this is the signal that settles any not yet
  // recorded as aborted.
  markEnded()
  await inner.stop(true)
  return { duration_ms: elapsed(started), requests, error, closed_by }
}

function startInner(start: () => Bun.Server<undefined>): Bun.Server<undefined> | Error {
  try {
    return start()
  } catch (error) {
    return error instanceof Error ? error : new Error(String(error))
  }
}

// What a request knows of the connection that carries it: how to break it, and how to hear that it
// has ended (the returned function stops listening).
type Carrier = { readonly kill: () => void; readonly onEnd: (abandon: () => void) => () => void }

// Settles once the socket has closed, including a close that happened before this was asked.
function closed(socket: net.Socket): Promise<'closed'> {
  return socket.closed ? Promise.resolve('closed') : new Promise(resolve => socket.once('close', () => resolve('closed')))
}

type Delivery = { readonly ended: 'complete' } | { readonly ended: 'failed' | 'aborted'; readonly why: string }

// One request: the response Bun.serve sends, and the promise that settles once the request's one
// record is emitted, which is after the response's body has ended one way or another.
// [LAW:no-silent-failure] Neither rejects: whatever goes wrong becomes the record's outcome.
function serveRequest<R extends EndpointRow>(
  table: EndpointTable<R>,
  generators: Generators<R>,
  emit: (event: ServerEvent) => void,
  kind: Kind,
  incoming: Request,
  carrier: Carrier,
  closer: () => Closer,
): { response: Promise<Response>; recorded: Promise<void> } {
  const started = performance.now()
  // [LAW:types-are-the-program] Every request on a connection is addressed to the connection's
  // authority: the URL is built from it, and the path and query are set as values, so neither the Host
  // header nor any target text (`//v1/messages` would parse as host `v1`) can choose the host. Bun.serve
  // gives the target joined to whatever the Host header said, so the scheme and authority are cut off
  // as text, which no Host value can make throw.
  const [path = '', query] = incoming.url.replace(/^[a-z][a-z0-9+.-]*:\/\/[^/?#]*/i, '').split(/\?(.*)/s)
  const url = new URL(`https://${kind.authority}`)
  url.pathname = path
  url.search = query ?? ''
  const { method } = incoming
  const resolution = resolveEndpoint(table, method, url)
  const endpoint = resolution?.endpoint.id ?? null
  const base = { event: 'request', tunnel: kind.tunnel, host: url.hostname, method, path: url.pathname, query: url.search, endpoint } as const
  let record!: (event: RequestEvent) => void
  let stopListening = () => {}
  const recorded = new Promise<void>(resolve => {
    record = event => {
      stopListening()
      emit(event)
      resolve()
    }
  })
  // [LAW:single-enforcer] [LAW:no-ambient-temporal-coupling] One signal ends a request early, in every
  // phase: the end of the connection that carries it. Over HTTP/1.1 a client abandons a request only by
  // closing its connection, and `stop()` and a failed body's kill close it too, so the mock's own event
  // covers every case and no signal of Bun.serve's is needed. Until a response exists, an abandoned
  // request is recorded here; after, its response's stream is cancelled and `observed` records it.
  const abandoned = new Promise<'abandoned'>(resolve => {
    stopListening = carrier.onEnd(() => resolve('abandoned'))
  })
  const unlessAbandoned = <T>(work: Promise<T>) => Promise.race([work, abandoned])
  const aborted = (before: string) => {
    record({ ...base, status: null, duration_ms: elapsed(started), outcome: 'aborted', why: endedEarly(closer(), before) })
    // The client is gone, or the mock is stopping; nothing reads this.
    return new Response(null)
  }
  const response = (async () => {
    // A body read fails only when its connection ends, which Bun.serve can see before the mock does:
    // the same signal, met through the read.
    const body = await unlessAbandoned(incoming.text()).catch(() => 'abandoned' as const)
    if (body === 'abandoned') return aborted(BODY_CUT_OFF)
    const dispatching = dispatch(generators, resolution, { method, url, headers: incoming.headers, body })
    const dispatched = await unlessAbandoned(dispatching)
    if (dispatched === 'abandoned') {
      // The response arrives with nobody to read it; cancelling its body stops whatever produces it.
      void dispatching.then(late => quietly(late.response.body?.cancel(endedEarly(closer(), NOT_READY))))
      return aborted(NOT_READY)
    }
    const { verdict, response } = dispatched
    return observed(method === 'HEAD' ? headersOnly(response) : response, carrier.kill, abandoned, closer, delivery => {
      const outcome = delivery.ended === 'complete' ? verdict : { outcome: delivery.ended, why: delivery.why }
      record({ ...base, status: response.status, duration_ms: elapsed(started), ...outcome })
    })
  })().catch((error: unknown) => {
    const why = `the mock failed: ${error instanceof Error ? error.message : String(error)}`
    record({ ...base, status: 500, duration_ms: elapsed(started), outcome: 'failed', why })
    return errorResponse(500, 'FAILED', { method, url }, why)
  })
  return { response, recorded }
}

// The response with its body passed through unbuffered, settling the delivery exactly once: complete
// when the body ends, failed when the source errors, aborted when the request is abandoned first (and
// the source is then cancelled, so whatever produces it stops). Bun.serve ends a chunked response
// cleanly when its stream errors, so a failure also kills the connection: the client sees a broken
// stream, not a truncated one that looks whole.
function observed(
  response: Response,
  kill: () => void,
  abandoned: Promise<'abandoned'>,
  closer: () => Closer,
  settle: (delivery: Delivery) => void,
): Response {
  if (response.body === null) {
    settle({ ended: 'complete' })
    return response
  }
  const reader = response.body.getReader()
  // A source error reaches the delivery through the read that meets it; `closed` rejects with the same
  // error a second time, and is handled so that second report is not taken for an unhandled one.
  reader.closed.catch(() => {})
  // The first ending is the delivery's: a read in flight when the request is abandoned still finishes,
  // and a failure's kill ends the connection after it. Whether this call was first.
  let ended = false
  const end = (delivery: Delivery): boolean => {
    if (ended) return false
    ended = true
    settle(delivery)
    return true
  }
  const body = new ReadableStream<Uint8Array>({
    async pull(controller) {
      const next = await reader.read().catch((error: unknown) => {
        if (end({ ended: 'failed', why: `response body failed: ${error instanceof Error ? error.message : String(error)}` })) kill()
        return null
      })
      if (next === null || ended) return
      if (next.done) {
        end({ ended: 'complete' })
        controller.close()
      } else {
        controller.enqueue(next.value)
      }
    },
  })
  // The body ends early when its request is abandoned; Bun.serve may also stop reading it, or cancel
  // this pass-through copy, but neither is relied on.
  async function abandon() {
    const why = endedEarly(closer(), BODY_UNFINISHED)
    if (end({ ended: 'aborted', why })) await quietly(reader.cancel(why))
  }
  void abandoned.then(abandon)
  return new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers })
}

// A response to HEAD carries the headers a GET would, and no body (RFC 9110 9.3.2). The generated body
// is cancelled, so whatever produces it stops.
function headersOnly(response: Response): Response {
  void quietly(response.body?.cancel('HEAD has no body'))
  return new Response(null, { status: response.status, statusText: response.statusText, headers: response.headers })
}

// A cancel is sent to a body nobody will read; its request's record already says how it ended, so a
// source whose cancel fails changes nothing and must not end the process as an unhandled rejection.
async function quietly(cancelling: Promise<void> | undefined): Promise<void> {
  await cancelling?.catch(() => {})
}

function elapsed(started: number): number {
  return Math.round(performance.now() - started)
}
