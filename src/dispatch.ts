import type { EndpointRow, EndpointTable, Resolution } from './endpoints.ts'
import { resolveEndpoint } from './endpoints.ts'

// PROJECT.md "Failure behavior": from one request to the response that answers it and the verdict on
// it. Every request the server receives, plain or tunneled, takes this one path.

// A request as a generator sees it. `url`'s host is the authority the request was addressed to: the
// CONNECT target for a tunneled request, api.anthropic.com for one sent straight to the port.
export type MockRequest = {
  readonly method: string
  readonly url: URL
  readonly headers: Headers
  readonly body: string
}

// A generator answers with a response, whose body may stream, or declines and says why: a Messages
// request no script matches is the generator's to refuse, not the table's.
export type Answer = { readonly kind: 'response'; readonly response: Response } | { readonly kind: 'declined'; readonly why: string }

export type Generator<R extends EndpointRow> = (resolution: Resolution<R>, request: MockRequest) => Answer | Promise<Answer>

// [LAW:types-are-the-program] Keyed by in-scope id only, so no generator can be written for a refused
// row, and each entry is typed by its own row, so it reads its params by name. Partial, because "an
// in-scope endpoint has no generator" is a state `coverage` exists to report.
export type Generators<R extends EndpointRow> = {
  readonly [Row in R as Row extends { scope: 'in-scope' } ? Row['id'] : never]?: Generator<Row>
}

export type Verdict =
  | { readonly outcome: 'served' }
  | { readonly outcome: 'unmatched'; readonly why: string }
  | { readonly outcome: 'failed'; readonly why: string }

export type Dispatched<R extends EndpointRow> = {
  readonly endpoint: R['id'] | null
  readonly verdict: Verdict
  readonly response: Response
}

// [LAW:dataflow-not-control-flow] Every request resolves, gets one verdict, and gets the response that
// verdict implies; only the values differ.
export async function dispatch<R extends EndpointRow>(
  table: EndpointTable<R>,
  generators: Generators<R>,
  request: MockRequest,
): Promise<Dispatched<R>> {
  const resolution = resolveEndpoint(table, request.method, request.url)
  const endpoint = resolution?.endpoint.id ?? null
  const decision = await decide(generators, resolution, request)
  switch (decision.kind) {
    case 'response':
      return { endpoint, verdict: { outcome: 'served' }, response: decision.response }
    case 'declined':
      return { endpoint, verdict: { outcome: 'unmatched', why: decision.why }, response: errorResponse(400, 'UNMATCHED', request, decision.why) }
    case 'failed':
      return { endpoint, verdict: { outcome: 'failed', why: decision.why }, response: errorResponse(500, 'FAILED', request, decision.why) }
  }
}

type Decision = Answer | { readonly kind: 'failed'; readonly why: string }

// The table's refusals are declines too: a request with no row, a row out of scope, and an in-scope
// row with no generator all reach the client as the same 400, each with its own reason.
async function decide<R extends EndpointRow>(
  generators: Generators<R>,
  resolution: Resolution<R> | null,
  request: MockRequest,
): Promise<Decision> {
  if (resolution === null) return { kind: 'declined', why: 'no endpoint' }
  const { endpoint } = resolution
  if (endpoint.scope === 'out-of-scope') return { kind: 'declined', why: `out of scope: ${endpoint.reason}` }
  // The key type and the row type are joined by Generators' mapping; indexing by a row's id reads the
  // generator for that row, which this cast is the one place to say.
  const generator = (generators as Partial<Record<string, Generator<R>>>)[endpoint.id]
  if (generator === undefined) return { kind: 'declined', why: 'no generator' }
  // [LAW:no-silent-failure] A generator that throws is the mock's own bug: a 500 that names it.
  try {
    return await generator(resolution, request)
  } catch (error) {
    return { kind: 'failed', why: error instanceof Error ? error.message : String(error) }
  }
}

// The Anthropic API's error envelope, so Claude Code shows the message, and `x-should-retry: false`,
// which the Anthropic SDK obeys, so a gap is one loud error and not a run of slow retries.
function errorResponse(status: 400 | 500, label: 'UNMATCHED' | 'FAILED', request: MockRequest, why: string): Response {
  const type = status === 400 ? 'invalid_request_error' : 'api_error'
  const message = `mock-anthropic ${label}: ${request.method} ${request.url.hostname} ${request.url.pathname}: ${why}`
  return Response.json(
    { type: 'error', error: { type, message } },
    { status, headers: { 'x-should-retry': 'false' } },
  )
}
