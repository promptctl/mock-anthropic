import type { accountGenerators, PlainRow } from './account-generators.ts'
import { type Answer, answer, type Generators, type Json } from './dispatch.ts'
import { parseCountTokensRequest } from './messages.ts'
import { inputTokens } from './render.ts'

// PROJECT.md "Every other in-scope endpoint": the answers that name no one. Feature flags, telemetry
// intake, organization settings and policy, the MCP directories, the plugin marketplace pointer, the
// changelog and token counting. Each body is the least both Claude Code builds in use accept: 2.1.88
// (its source) and 2.1.289 (its shipped binary). Where a value turns a feature on or off, it is the one
// that keeps Claude Code from calling an endpoint the table refuses.

// [LAW:one-type-per-behavior] A fixed answer: the same response to every request, with nothing decided
// to report. A Response is read once, so each request gets a new one.
const fixed = (make: () => Response) => (): Answer => ({ kind: 'response', response: make(), facts: {} })
const json = (body: Json) => (): Answer => answer(body, {})
const text = (body: string) => fixed(() => new Response(body, { headers: { 'content-type': 'text/plain; charset=utf-8' } }))

// The marketplace commit the version pointer names, obviously not a real one.
const MARKETPLACE_SHA = '0'.repeat(40)

// [LAW:single-enforcer] The plain rows the account map does not answer. A row given a generator in
// both maps fails here, whichever map gained it, so neither can silently replace the other's.
type ServiceRow = Exclude<PlainRow, { id: keyof ReturnType<typeof accountGenerators> }>

export function serviceGenerators() {
  return {
    'messages-count-tokens': (_, request) => {
      const parsed = parseCountTokensRequest(request.body)
      if (parsed.kind === 'invalid') return { kind: 'declined', why: parsed.why, facts: {} }
      const input_tokens = inputTokens(parsed.value)
      // [LAW:nothing-unseen] The count given, for the record.
      return answer({ input_tokens }, { input_tokens })
    },
    // Every flag at its default. Both builds read an empty map as no payload, and keep their defaults.
    'feature-flag-eval': json({ features: {} }),
    // Neither build reads these answers, and 2.1.289 may gzip what it sends, so the body is not read.
    'event-logging-batch': json({}),
    'event-logging-v2-batch': json({}),
    'datadog-logs': json({}),
    // Off: on, 2.1.289's BigQuery exporter posts to the refused `otel-metrics` row.
    'metrics-enabled': json({ metrics_logging_enabled: false }),
    // No settings: both builds read a 204 as none configured.
    'remote-managed-settings': fixed(() => new Response(null, { status: 204 })),
    // No restrictions. 2.1.289 reads a 404 here as a proxy that did not forward the request.
    'policy-limits': json({ restrictions: {} }),
    'penguin-mode': json({ enabled: false, disabled_reason: null }),
    // Off, so the consent notice never runs, nor its refused `account-settings` call.
    'grove-config': json({ grove_enabled: false, domain_excluded: false, notice_is_grace_period: true, notice_reminder_frequency: null }),
    // No servers. 2.1.289 asks for the next page while `metadata.nextCursor` is set.
    'mcp-registry': json({ servers: [], metadata: {} }),
    'mcp-servers': json({ data: [], next_page: null }),
    // Claude Code next fetches `{sha}.zip`, which the table refuses, unless the config dir it runs
    // with says the official marketplace is installed or its auto-install already ran.
    'marketplace-version': text(`${MARKETPLACE_SHA}\n`),
    // No `## ` section, so no release notes.
    changelog: text('# Changelog\n'),
  } satisfies Generators<ServiceRow>
}
