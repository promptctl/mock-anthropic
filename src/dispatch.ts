import type { EndpointRow, Resolution } from './endpoints.ts'

// PROJECT.md "Failure behavior": from one resolved request to the response that answers it and the
// verdict on it. Every request the server receives, plain or tunneled, takes this one path.

// A request as a generator sees it. `url`'s host is the authority the request was addressed to: the
// CONNECT target for a tunneled request, api.anthropic.com for one sent straight to the port.
export type MockRequest = {
  readonly method: string
  readonly url: URL
  readonly headers: Headers
  readonly body: string
}

export type Json = string | number | boolean | null | readonly Json[] | { readonly [key: string]: Json }

// [LAW:nothing-unseen] What a generator decided on the way to its answer, for the request's record:
// for a Messages request, the script that answered and what was rendered, or every script tried.
// [LAW:composability] One JSON value whose fields each generator types for itself, so this seam names
// no endpoint's concepts. `{}` is a generator that has nothing to report.
export type Facts = { readonly [key: string]: Json }

// A generator answers with a response, whose body may stream, or declines and says why: a Messages
// request no script matches is the generator's to refuse, not the table's. Either way it says what
// it decided.
export type Answer = (
  | { readonly kind: 'response'; readonly response: Response }
  | { readonly kind: 'declined'; readonly why: string }
) & { readonly facts: Facts }

// The answer most generators give: a JSON body, with what was decided to reach it.
export const answer = (body: Json, facts: Facts): Answer => ({ kind: 'response', response: Response.json(body), facts })

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

// The facts sit beside the verdict, not in it: they describe the answer, and stay true of it when its
// body later fails or is abandoned and the record's outcome changes.
export type Dispatched = { readonly verdict: Verdict; readonly response: Response; readonly facts: Facts }

// [LAW:dataflow-not-control-flow] Every request gets one verdict and the response that verdict implies;
// only the values differ. [LAW:one-source-of-truth] The resolution is the caller's, made once, so the
// endpoint every record of the request names is the one that chose its generator.
export async function dispatch<R extends EndpointRow>(
  generators: Generators<R>,
  resolution: Resolution<R> | null,
  request: MockRequest,
): Promise<Dispatched> {
  const decision = await decide(generators, resolution, request)
  const { facts } = decision
  switch (decision.kind) {
    case 'response':
      return { verdict: { outcome: 'served' }, response: decision.response, facts }
    case 'declined':
      return { verdict: { outcome: 'unmatched', why: decision.why }, response: errorResponse('UNMATCHED', request, decision.why), facts }
    case 'failed':
      return { verdict: { outcome: 'failed', why: decision.why }, response: errorResponse('FAILED', request, decision.why), facts }
  }
}

// The table's refusals and a generator that throws decided nothing a generator reports.
type Decision = Answer | { readonly kind: 'failed'; readonly why: string; readonly facts: Facts }
const NO_FACTS: Facts = {}

// The table's refusals are declines too: a request with no row, a row out of scope, and an in-scope
// row with no generator all reach the client as the same 400, each with its own reason.
async function decide<R extends EndpointRow>(
  generators: Generators<R>,
  resolution: Resolution<R> | null,
  request: MockRequest,
): Promise<Decision> {
  if (resolution === null) return { kind: 'declined', why: 'no endpoint', facts: NO_FACTS }
  const { endpoint } = resolution
  if (endpoint.scope === 'out-of-scope') return { kind: 'declined', why: `out of scope: ${endpoint.reason}`, facts: NO_FACTS }
  // The key type and the row type are joined by Generators' mapping; indexing by a row's id reads the
  // generator for that row, which this cast is the one place to say.
  const generator = (generators as Partial<Record<string, Generator<R>>>)[endpoint.id]
  if (generator === undefined) return { kind: 'declined', why: 'no generator', facts: NO_FACTS }
  // [LAW:no-silent-failure] A generator that throws is the mock's own bug: a 500 that names it.
  try {
    return await generator(resolution, request)
  } catch (error) {
    return { kind: 'failed', why: error instanceof Error ? error.message : String(error), facts: NO_FACTS }
  }
}

// The Anthropic API's error envelope, so Claude Code shows the message, and `x-should-retry: false`,
// which the Anthropic SDK obeys, so a gap is one loud error and not a run of slow retries. Every error
// response the mock makes for a request is this one, and its label decides its status and type.
const ERRORS = {
  UNMATCHED: { status: 400, type: 'invalid_request_error' },
  FAILED: { status: 500, type: 'api_error' },
} as const

export function errorResponse(label: keyof typeof ERRORS, request: Pick<MockRequest, 'method' | 'url'>, why: string): Response {
  const { status, type } = ERRORS[label]
  const message = `mock-anthropic ${label}: ${request.method} ${request.url.hostname} ${request.url.pathname}: ${why}`
  return Response.json(
    { type: 'error', error: { type, message } },
    { status, headers: { 'x-should-retry': 'false' } },
  )
}
