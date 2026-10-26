import { z } from 'zod'

// A request body a generator reads: JSON, then the schema of what the endpoint accepts.
// [LAW:parse-dont-validate] The one checkpoint a JSON body crosses. A body that is not one is the
// generator's to decline, with the reason, so the result names `what` the body failed to be.
export type ParsedBody<T> = { readonly kind: 'parsed'; readonly value: T } | { readonly kind: 'invalid'; readonly why: string }

export function parseJsonBody<S extends z.ZodType>(raw: string, schema: S, what: string): ParsedBody<z.output<S>> {
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch (error) {
    return { kind: 'invalid', why: `body is not JSON: ${String(error)}` }
  }
  const result = schema.safeParse(value)
  return result.success ? { kind: 'parsed', value: result.data } : { kind: 'invalid', why: `body is not ${what}: ${z.prettifyError(result.error)}` }
}
