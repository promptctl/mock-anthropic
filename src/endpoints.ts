// PROJECT.md "Endpoint table": every endpoint Claude Code is known to call, each in scope (a generator
// answers it) or out of scope (refused with its reason), and the resolver from a request to its row.
// [LAW:one-source-of-truth] The one list of hosts and paths, for server routing, `coverage`, the
// CONNECT host allowlist and the certificate's host list.

export type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

type Route = { id: string; host: string; method: Method; path: string }

// [LAW:types-are-the-program] Scope is the row's top-level discriminant, so checking `scope` narrows a
// row of the real table to its literal type, and an in-scope row's `id` to an InScopeId. An
// out-of-scope row cannot be written without its reason.
export type EndpointRow = Route & ({ scope: 'in-scope' } | { scope: 'out-of-scope'; reason: string })

// `const` keeps each path's literal type, so ParamName can read a row's params from it.
function inScope<const Id extends string, const Path extends string>(id: Id, host: string, method: Method, path: Path) {
  return { id, host, method, path, scope: 'in-scope' } as const
}

function outOfScope<const Id extends string, const Path extends string>(
  id: Id,
  host: string,
  method: Method,
  path: Path,
  reason: string,
) {
  return { id, host, method, path, scope: 'out-of-scope', reason } as const
}

const API = 'api.anthropic.com'
const PLATFORM = 'platform.claude.com'
const CLAUDE_AI = 'claude.ai'
const DOWNLOADS = 'downloads.claude.ai'
const GITHUB_RAW = 'raw.githubusercontent.com'
const DATADOG = 'http-intake.logs.us5.datadoghq.com'
const MCP_PROXY = 'mcp-proxy.anthropic.com'

// Reasons many rows share, one constant each.
const REMOTE = 'remote sessions, bridge and teleport: a mock session runs locally'
const BROWSER_LOGIN = 'browser login flow: `login` writes a config dir the mock accepts instead'
const API_KEY_LOGIN = 'API-key login: the mock logs Claude Code in with OAuth'
const CONSENT = 'data-sharing consent prompt'
const BILLING = 'billing, referral and extra-usage surfaces'
const FEEDBACK = 'feedback upload'
const TRIGGERS = 'scheduled cloud agents'
const PREFLIGHT = '`claude doctor` connectivity preflight'
const CONNECTORS = 'claude.ai connectors'

const ROWS = [
  inScope('messages', API, 'POST', '/v1/messages'),
  inScope('messages-count-tokens', API, 'POST', '/v1/messages/count_tokens'),

  inScope('oauth-profile', API, 'GET', '/api/oauth/profile'),
  inScope('oauth-roles', API, 'GET', '/api/oauth/claude_cli/roles'),
  inScope('oauth-usage', API, 'GET', '/api/oauth/usage'),
  inScope('oauth-validate', API, 'POST', '/api/oauth/validate'),
  inScope('oauth-token', PLATFORM, 'POST', '/v1/oauth/token'),
  inScope('claude-cli-bootstrap', API, 'GET', '/api/claude_cli/bootstrap'),
  inScope('feature-flag-eval', API, 'POST', '/api/eval/{client_key}'),
  inScope('event-logging-batch', API, 'POST', '/api/event_logging/batch'),
  inScope('event-logging-v2-batch', API, 'POST', '/api/event_logging/v2/batch'),
  inScope('metrics-enabled', API, 'GET', '/api/claude_code/organizations/metrics_enabled'),
  inScope('remote-managed-settings', API, 'GET', '/api/claude_code/settings'),
  inScope('policy-limits', API, 'GET', '/api/claude_code/policy_limits'),
  inScope('penguin-mode', API, 'GET', '/api/claude_code_penguin_mode'),
  inScope('grove-config', API, 'GET', '/api/claude_code_grove'),
  inScope('referral-eligibility', API, 'GET', '/api/oauth/organizations/{org}/referral/eligibility'),
  inScope('overage-credit-grant', API, 'GET', '/api/oauth/organizations/{org}/overage_credit_grant'),
  inScope('mcp-registry', API, 'GET', '/mcp-registry/v0/servers'),
  inScope('mcp-servers', API, 'GET', '/v1/mcp_servers'),
  inScope('marketplace-version', DOWNLOADS, 'GET', '/claude-code-releases/plugins/claude-plugins-official/latest'),
  inScope('changelog', GITHUB_RAW, 'GET', '/anthropics/claude-code/refs/heads/main/CHANGELOG.md'),

  // Claude Code 2.1.289's additions.
  inScope('organization-skills', API, 'GET', '/api/oauth/organizations/{org}/skills/list-skills'),
  inScope('model-selector', API, 'GET', '/api/organizations/{org}/model_selector/cc'),
  inScope('organization-marketplaces', API, 'GET', '/api/oauth/organizations/{org}/marketplaces'),
  inScope('organization-plugins', API, 'GET', '/api/oauth/organizations/{org}/plugins/list-plugins'),
  inScope('datadog-logs', DATADOG, 'POST', '/api/v2/logs'),

  outOfScope('claude-cli-profile', API, 'GET', '/api/claude_cli_profile', API_KEY_LOGIN),
  outOfScope('oauth-authorize', PLATFORM, 'GET', '/oauth/authorize', BROWSER_LOGIN),
  outOfScope('oauth-create-api-key', API, 'POST', '/api/oauth/claude_cli/create_api_key', BROWSER_LOGIN),
  outOfScope('api-hello', API, 'GET', '/api/hello', PREFLIGHT),
  outOfScope('oauth-hello', PLATFORM, 'GET', '/v1/oauth/hello', PREFLIGHT),

  outOfScope('user-settings', API, 'GET', '/api/claude_code/user_settings', 'settings sync'),
  outOfScope('user-settings-put', API, 'PUT', '/api/claude_code/user_settings', 'settings sync'),
  outOfScope('team-memory', API, 'GET', '/api/claude_code/team_memory', 'team memory sync'),
  outOfScope('team-memory-put', API, 'PUT', '/api/claude_code/team_memory', 'team memory sync'),
  outOfScope('otel-metrics', API, 'POST', '/api/claude_code/metrics', 'opt-in OpenTelemetry metrics export'),

  outOfScope('grove', API, 'POST', '/api/claude_code_grove', CONSENT),
  outOfScope('account-settings', API, 'GET', '/api/oauth/account/settings', CONSENT),
  outOfScope('account-settings-post', API, 'POST', '/api/oauth/account/settings', CONSENT),
  outOfScope('grove-notice-viewed', API, 'POST', '/api/oauth/account/grove_notice_viewed', CONSENT),

  outOfScope('referral-redemptions', API, 'GET', '/api/oauth/organizations/{org}/referral/redemptions', BILLING),
  outOfScope('admin-requests', API, 'POST', '/api/oauth/organizations/{org}/admin_requests', BILLING),
  outOfScope('admin-requests-me', API, 'GET', '/api/oauth/organizations/{org}/admin_requests/me', BILLING),
  outOfScope(
    'admin-requests-eligibility',
    API,
    'GET',
    '/api/oauth/organizations/{org}/admin_requests/eligibility',
    BILLING,
  ),
  outOfScope('first-token-date', API, 'GET', '/api/organization/claude_code_first_token_date', BILLING),
  outOfScope('cloud-credit', API, 'GET', '/v1/code/promo/cloud_credit', BILLING),

  outOfScope('mcp-start-auth', API, 'POST', '/api/organizations/{org}/mcp/start-auth/{server_id}', CONNECTORS),
  // MCP streamable HTTP: POST sends a message, GET opens the server's event stream, DELETE ends the session.
  outOfScope('mcp-proxy', MCP_PROXY, 'POST', '/v1/mcp/{server_id}', CONNECTORS),
  outOfScope('mcp-proxy-get', MCP_PROXY, 'GET', '/v1/mcp/{server_id}', CONNECTORS),
  outOfScope('mcp-proxy-delete', MCP_PROXY, 'DELETE', '/v1/mcp/{server_id}', CONNECTORS),
  outOfScope('mcp-client-metadata', CLAUDE_AI, 'GET', '/oauth/claude-code-client-metadata', CONNECTORS),
  outOfScope('connector-directory', API, 'GET', '/api/directory/servers', CONNECTORS),

  outOfScope(
    'organization-skill-download',
    API,
    'GET',
    '/api/oauth/organizations/{org}/skills/{skill}/download',
    'organization skill download',
  ),
  outOfScope(
    'marketplace-archive',
    DOWNLOADS,
    'GET',
    '/claude-code-releases/plugins/claude-plugins-official/{sha}.zip',
    'plugin marketplace archive: a binary download',
  ),
  outOfScope(
    'plugin-install-counts',
    GITHUB_RAW,
    'GET',
    '/anthropics/claude-plugins-official/refs/heads/stats/stats/plugin-installs.json',
    'plugin marketplace install counts',
  ),

  outOfScope('frame-contract', API, 'GET', '/api/frame/contract/latest', 'Frame web-app publishing'),
  outOfScope('web-domain-info', API, 'GET', '/api/web/domain_info', 'WebFetch tool domain check'),
  outOfScope('cli-feedback', API, 'POST', '/api/claude_cli_feedback', FEEDBACK),
  outOfScope('shared-session-transcripts', API, 'POST', '/api/claude_code_shared_session_transcripts', FEEDBACK),
  outOfScope('file-upload', API, 'POST', '/api/oauth/file_upload', 'Brief tool attachment upload'),
  outOfScope('voice-stream', API, 'GET', '/api/ws/speech_to_text/voice_stream', 'voice mode: a WebSocket'),

  outOfScope('file-content', API, 'GET', '/api/oauth/files/{file_id}/content', REMOTE),
  outOfScope('trusted-devices', API, 'POST', '/api/auth/trusted_devices', REMOTE),
  outOfScope('code-sessions', API, 'POST', '/v1/code/sessions', REMOTE),
  outOfScope('code-session', API, 'GET', '/v1/code/sessions/{session_id}', REMOTE),
  outOfScope('code-session-put', API, 'PUT', '/v1/code/sessions/{session_id}', REMOTE),
  outOfScope('code-session-archive', API, 'POST', '/v1/code/sessions/{session_id}/archive', REMOTE),
  outOfScope('code-session-bridge', API, 'POST', '/v1/code/sessions/{session_id}/bridge', REMOTE),
  outOfScope('code-session-worker', API, 'PUT', '/v1/code/sessions/{session_id}/worker', REMOTE),
  outOfScope('code-session-worker-heartbeat', API, 'POST', '/v1/code/sessions/{session_id}/worker/heartbeat', REMOTE),
  outOfScope('code-session-worker-events', API, 'POST', '/v1/code/sessions/{session_id}/worker/events', REMOTE),
  outOfScope(
    'code-session-worker-events-delivery',
    API,
    'POST',
    '/v1/code/sessions/{session_id}/worker/events/delivery',
    REMOTE,
  ),
  outOfScope('code-session-client-presence', API, 'POST', '/v1/code/sessions/{session_id}/client/presence', REMOTE),
  outOfScope('code-session-teleport-events', API, 'POST', '/v1/code/sessions/{session_id}/teleport-events', REMOTE),
  outOfScope('environment-work-poll', API, 'GET', '/v1/environments/{env_id}/work/poll', REMOTE),
  outOfScope('environment-work-heartbeat', API, 'POST', '/v1/environments/{env_id}/work/{session_id}/heartbeat', REMOTE),
  outOfScope('environment-providers', API, 'GET', '/v1/environment_providers', REMOTE),
  outOfScope('environment-providers-cloud-create', API, 'POST', '/v1/environment_providers/cloud/create', REMOTE),
  outOfScope('sessions', API, 'POST', '/v1/sessions', REMOTE),
  outOfScope('sessions-list', API, 'GET', '/v1/sessions', REMOTE),
  outOfScope('session', API, 'GET', '/v1/sessions/{session_id}', REMOTE),
  outOfScope('session-events', API, 'GET', '/v1/sessions/{session_id}/events', REMOTE),
  outOfScope('session-archive', API, 'POST', '/v1/sessions/{session_id}/archive', REMOTE),
  outOfScope('github-import-token', API, 'POST', '/v1/code/github/import-token', REMOTE),
  outOfScope('code-repos', API, 'GET', '/api/oauth/organizations/{org}/code/repos/{owner}/{repo}', REMOTE),
  outOfScope('github-sync-auth', API, 'GET', '/api/oauth/organizations/{org}/sync/github/auth', REMOTE),
  outOfScope('upstream-proxy-ca-cert', API, 'GET', '/v1/code/upstreamproxy/ca-cert', REMOTE),
  outOfScope('upstream-proxy-ws', API, 'GET', '/v1/code/upstreamproxy/ws', REMOTE),

  outOfScope('code-triggers', API, 'GET', '/v1/code/triggers', TRIGGERS),
  outOfScope('code-triggers-create', API, 'POST', '/v1/code/triggers', TRIGGERS),
  outOfScope('code-trigger', API, 'GET', '/v1/code/triggers/{trigger_id}', TRIGGERS),
  outOfScope('code-trigger-update', API, 'PATCH', '/v1/code/triggers/{trigger_id}', TRIGGERS),
  outOfScope('code-trigger-delete', API, 'DELETE', '/v1/code/triggers/{trigger_id}', TRIGGERS),
  outOfScope('code-trigger-run', API, 'POST', '/v1/code/triggers/{trigger_id}/run', TRIGGERS),
]

type Row = (typeof ROWS)[number]
export type EndpointId = Row['id']
export type InScopeId = Extract<Row, { scope: 'in-scope' }>['id']

// One segment of a path template. A param matches any one segment that starts with its prefix, ends
// with its suffix, and has at least one character between them: `{org}` has neither, `{sha}.zip` has
// the suffix `.zip`.
type ParamSegment = { kind: 'param'; name: string; prefix: string; suffix: string }
export type Segment = { kind: 'literal'; text: string } | ParamSegment

// The names of a path template's params, read from its type: `/x/{org}/{sha}.zip` gives `org | sha`.
export type ParamName<Path extends string> = string extends Path
  ? string
  : Path extends `${string}{${infer Name}}${infer Rest}`
    ? Name | ParamName<Rest>
    : never

export type Endpoint<R extends EndpointRow = Row> = R & { readonly segments: readonly Segment[] }

declare const parsed: unique symbol

// [LAW:parse-dont-validate] An EndpointTable exists only as buildEndpointTable's output, so the
// resolver never meets a table whose templates were not parsed or whose rows overlap.
export type EndpointTable<R extends EndpointRow = Row> = {
  readonly endpoints: readonly Endpoint<R>[]
  readonly [parsed]: true
}

const PARAM_SEGMENT = /^([^{}]*)\{([a-z_]+)\}([^{}]*)$/

// A segment of a URL's pathname, percent-decoded: the one form every template segment is matched
// against. Null when its escapes do not decode.
function requestSegment(raw: string): string | null {
  try {
    return decodeURIComponent(raw)
  } catch {
    return null
  }
}

// The text a request for `/${text}` is matched as: what a WHATWG URL makes of it, then decoded.
const asRequested = (text: string) => requestSegment(new URL(`https://host/${text}`).pathname.slice(1))

function parseSegment(text: string, row: EndpointRow): Segment {
  if (text === '') throw new Error(`endpoint ${row.id}: ${row.path} has an empty segment`)
  const segment = parseSegmentSyntax(text, row)
  // A param stands for at least one character, so `x` stands in for it here.
  const sample = segment.kind === 'literal' ? segment.text : `${segment.prefix}x${segment.suffix}`
  if (asRequested(sample) !== sample) {
    throw new Error(`endpoint ${row.id}: segment ${text} of ${row.path} is not what a request for it is matched as`)
  }
  return segment
}

function parseSegmentSyntax(text: string, row: EndpointRow): Segment {
  if (!/[{}]/.test(text)) return Object.freeze({ kind: 'literal', text })
  const match = PARAM_SEGMENT.exec(text)
  if (match === null) {
    throw new Error(`endpoint ${row.id}: segment ${text} of ${row.path} is not one {name} of [a-z_] with fixed text around it`)
  }
  const [, prefix = '', name = '', suffix = ''] = match
  return Object.freeze({ kind: 'param', name, prefix, suffix })
}

function parseTemplate(row: EndpointRow): readonly Segment[] {
  if (!row.path.startsWith('/')) throw new Error(`endpoint ${row.id}: ${row.path} does not start with /`)
  const segments = row.path.slice(1).split('/').map(text => parseSegment(text, row))
  const names = segments.flatMap(segment => (segment.kind === 'param' ? [segment.name] : []))
  for (const name of repeated(names)) throw new Error(`endpoint ${row.id}: ${row.path} names {${name}} twice`)
  return Object.freeze(segments)
}

function parseEndpoint<R extends EndpointRow>(row: R): Endpoint<R> {
  if (new URL(`https://${row.host}`).hostname !== row.host) {
    throw new Error(`endpoint ${row.id}: host ${row.host} is not in the form a URL gives it, lowercase with no port`)
  }
  if (row.scope === 'out-of-scope' && !/\S/.test(row.reason)) {
    throw new Error(`endpoint ${row.id}: an out-of-scope row needs a reason`)
  }
  const endpoint: Endpoint<R> = { ...row, segments: parseTemplate(row) }
  Object.freeze(endpoint)
  return endpoint
}

// Each value that occurs more than once, once.
function repeated(values: readonly string[]): string[] {
  return [...new Set(values.filter((value, i) => values.indexOf(value) !== i))]
}

// [LAW:one-source-of-truth] The one matching rule, for the resolver and the overlap check alike,
// applied to a decoded request segment.
function segmentMatches(segment: Segment, text: string): boolean {
  switch (segment.kind) {
    case 'literal':
      return segment.text === text
    case 'param':
      return (
        text.length > segment.prefix.length + segment.suffix.length &&
        text.startsWith(segment.prefix) &&
        text.endsWith(segment.suffix)
      )
  }
}

// Each param's value, its prefix and suffix removed, when every request segment matches its template
// segment; else null.
function matchSegments(segments: readonly Segment[], texts: readonly string[]): Record<string, string> | null {
  const pairs = zip(segments, texts)
  if (pairs === null || !pairs.every(([segment, text]) => segmentMatches(segment, text))) return null
  const params = pairs.flatMap(([segment, text]) =>
    segment.kind === 'param' ? [[segment.name, text.slice(segment.prefix.length, text.length - segment.suffix.length)]] : [],
  )
  return Object.fromEntries(params)
}

// Whether one request segment could match both. Two params share a match exactly when their prefixes
// agree as far as the shorter one goes, and their suffixes too: the middle can then be made long enough
// for both.
function segmentsOverlap(a: Segment, b: Segment): boolean {
  if (a.kind === 'literal') return segmentMatches(b, a.text)
  if (b.kind === 'literal') return segmentMatches(a, b.text)
  const prefixesAgree = a.prefix.startsWith(b.prefix) || b.prefix.startsWith(a.prefix)
  const suffixesAgree = a.suffix.endsWith(b.suffix) || b.suffix.endsWith(a.suffix)
  return prefixesAgree && suffixesAgree
}

// The pairs at each position of two lists of one length, or null when their lengths differ.
function zip<A, B>(xs: readonly A[], ys: readonly B[]): (readonly [A, B])[] | null {
  // ys[i] is in range: the lengths are equal.
  return xs.length === ys.length ? xs.map((x, i) => [x, ys[i] as B] as const) : null
}

function overlap(a: Endpoint<EndpointRow>, b: Endpoint<EndpointRow>): boolean {
  const pairs = zip(a.segments, b.segments)
  return a.host === b.host && a.method === b.method && pairs !== null && pairs.every(([x, y]) => segmentsOverlap(x, y))
}

// [LAW:single-enforcer] The one checkpoint a table passes: every row parses, every id is unique, and
// no request can match two rows, which is what lets the resolver return the first match it finds.
// Frozen, so nothing can add an overlapping row after the check.
export function buildEndpointTable<R extends EndpointRow>(rows: readonly R[]): EndpointTable<R> {
  const endpoints = Object.freeze(rows.map(parseEndpoint))
  const problems = [
    ...repeated(endpoints.map(endpoint => endpoint.id)).map(id => `duplicate endpoint id ${id}`),
    ...endpoints.flatMap((a, i) =>
      endpoints
        .slice(i + 1)
        .filter(b => overlap(a, b))
        .map(b => `endpoints ${a.id} and ${b.id} overlap: ${a.method} ${a.host} ${a.path} and ${b.path}`),
    ),
  ]
  if (problems.length > 0) throw new Error(`endpoint table:\n${problems.join('\n')}`)
  // The brand is a type-level stamp with no runtime value; this cast is the one place it is applied.
  return Object.freeze({ endpoints }) as unknown as EndpointTable<R>
}

export const ENDPOINT_TABLE: EndpointTable = buildEndpointTable(ROWS)

// Every host the table knows, in table order, for the CONNECT allowlist and the certificate's host list.
export const ENDPOINT_HOSTS: readonly string[] = Object.freeze([
  ...new Set(ENDPOINT_TABLE.endpoints.map(endpoint => endpoint.host)),
])

// The row a request is addressed to, and each of the row's params with the value its segment gave it,
// percent-decoded and its prefix and suffix removed. One member per row, so a generator typed by its
// row reads its params by name. A URL's hostname is already lowercase with no port, and its pathname
// has no query.
export type Resolution<R extends EndpointRow = Row> = R extends EndpointRow
  ? { readonly endpoint: Endpoint<R>; readonly params: Readonly<Record<ParamName<R['path']>, string>> }
  : never

// The resolution of one row of the endpoint table, by id: what a generator for that row receives.
export type ResolutionOf<Id extends EndpointId> = Resolution<Extract<Row, { id: Id }>>

export function resolveEndpoint<R extends EndpointRow>(
  table: EndpointTable<R>,
  method: string,
  url: URL,
): Resolution<R> | null {
  const texts = url.pathname.slice(1).split('/').map(requestSegment)
  // A path with an escape that does not decode addresses no row.
  if (!texts.every(text => text !== null)) return null
  for (const endpoint of table.endpoints) {
    if (endpoint.host !== url.hostname || endpoint.method !== method) continue
    const params = matchSegments(endpoint.segments, texts)
    // The params are exactly the {name}s parseTemplate read from this row's path, which are the names
    // ParamName reads from its type; this cast is the one place the two are joined.
    if (params !== null) return { endpoint, params } as unknown as Resolution<R>
  }
  return null
}
