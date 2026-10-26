// PROJECT.md "Endpoint table": every endpoint Claude Code is known to call, each in scope (a generator
// answers it) or out of scope (refused with its reason), and the resolver from a request to its row.
// [LAW:one-source-of-truth] The one list of hosts and paths: server routing, `coverage`, the CONNECT
// host allowlist and the certificate's host list all read it.

export type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

type Route = { id: string; host: string; method: Method; path: string }

// [LAW:types-are-the-program] Scope is the row's top-level discriminant, so checking `scope` narrows a
// row of the real table to its literal type, and an in-scope row's `id` to an InScopeId. An
// out-of-scope row cannot be written without its reason.
export type EndpointRow = Route & ({ scope: 'in-scope' } | { scope: 'out-of-scope'; reason: string })

function inScope<const Id extends string>(id: Id, host: string, method: Method, path: string) {
  return { id, host, method, path, scope: 'in-scope' } as const
}

function outOfScope<const Id extends string>(id: Id, host: string, method: Method, path: string, reason: string) {
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
  outOfScope('mcp-proxy', MCP_PROXY, 'POST', '/v1/mcp/{server_id}', CONNECTORS),
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
export type Segment =
  | { kind: 'literal'; text: string }
  | { kind: 'param'; name: string; prefix: string; suffix: string }

export type Endpoint<R extends EndpointRow = Row> = R & { readonly segments: readonly Segment[] }

declare const parsed: unique symbol

// [LAW:parse-dont-validate] An EndpointTable exists only as buildEndpointTable's output, so the
// resolver never meets a table whose templates were not parsed or whose rows overlap.
export type EndpointTable<R extends EndpointRow = Row> = {
  readonly endpoints: readonly Endpoint<R>[]
  readonly [parsed]: true
}

const PARAM_SEGMENT = /^([^{}]*)\{([a-z_]+)\}([^{}]*)$/

function parseSegment(text: string, row: EndpointRow): Segment {
  if (text === '') throw new Error(`endpoint ${row.id}: ${row.path} has an empty segment`)
  if (!/[{}]/.test(text)) return { kind: 'literal', text }
  const match = PARAM_SEGMENT.exec(text)
  if (match === null) {
    throw new Error(`endpoint ${row.id}: segment ${text} of ${row.path} is not one {name} of [a-z_] with fixed text around it`)
  }
  const [, prefix = '', name = '', suffix = ''] = match
  return { kind: 'param', name, prefix, suffix }
}

function parseTemplate(row: EndpointRow): Segment[] {
  if (!row.path.startsWith('/')) throw new Error(`endpoint ${row.id}: ${row.path} does not start with /`)
  return row.path.slice(1).split('/').map(text => parseSegment(text, row))
}

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

// Whether two lists have the same length and every pair at one position satisfies `test`.
function pairwise<A, B>(xs: readonly A[], ys: readonly B[], test: (x: A, y: B) => boolean): boolean {
  return (
    xs.length === ys.length &&
    xs.every((x, i) => {
      const y = ys[i]
      return y !== undefined && test(x, y)
    })
  )
}

function overlap(a: Endpoint<EndpointRow>, b: Endpoint<EndpointRow>): boolean {
  return a.host === b.host && a.method === b.method && pairwise(a.segments, b.segments, segmentsOverlap)
}

// [LAW:single-enforcer] The one checkpoint a table passes: every template parses, every id is unique,
// and no request can match two rows, which is what lets the resolver return the first match it finds.
export function buildEndpointTable<R extends EndpointRow>(rows: readonly R[]): EndpointTable<R> {
  const endpoints = rows.map(row => ({ ...row, segments: parseTemplate(row) }))
  const problems = endpoints.flatMap((a, i) =>
    endpoints.slice(i + 1).flatMap(b => [
      ...(a.id === b.id ? [`duplicate endpoint id ${a.id}`] : []),
      ...(overlap(a, b) ? [`endpoints ${a.id} and ${b.id} overlap: ${a.method} ${a.host} ${a.path} and ${b.path}`] : []),
    ]),
  )
  if (problems.length > 0) throw new Error(`endpoint table:\n${problems.join('\n')}`)
  // The brand is a type-level stamp with no runtime value; this cast is the one place it is applied.
  return { endpoints } as unknown as EndpointTable<R>
}

export const ENDPOINT_TABLE: EndpointTable = buildEndpointTable(ROWS)

// The row a request is addressed to, or null when no row is. `host` is a bare hostname and `path` has
// no query string; normalizing either is the server's job at its boundary.
export function resolveEndpoint<R extends EndpointRow>(
  table: EndpointTable<R>,
  host: string,
  method: string,
  path: string,
): Endpoint<R> | null {
  const segments = path.split('/').slice(1)
  const matches = (endpoint: Endpoint<R>) =>
    endpoint.host === host && endpoint.method === method && pairwise(endpoint.segments, segments, segmentMatches)
  return table.endpoints.find(matches) ?? null
}
