import { describe, expect, test } from 'bun:test'
import { DEFAULT_ACCOUNT } from './account.ts'
import { buildEndpointTable, ENDPOINT_TABLE, type EndpointRow, type InScopeId, resolveEndpoint } from './endpoints.ts'

const API = 'api.anthropic.com'
const ORG = DEFAULT_ACCOUNT.organizationUuid
const resolve = (host: string, method: string, path: string) => resolveEndpoint(ENDPOINT_TABLE, host, method, path)

describe('resolveEndpoint over the endpoint table', () => {
  test('a templated organization path resolves to its row', () => {
    expect(resolve(API, 'GET', `/api/oauth/organizations/${ORG}/referral/eligibility`)?.id).toBe('referral-eligibility')
  })

  const nearMisses: [string, string, string][] = [
    ['an extra segment', 'GET', `/api/oauth/organizations/${ORG}/referral/eligibility/x`],
    ['a missing segment', 'GET', `/api/oauth/organizations/${ORG}/referral`],
    ['a trailing slash', 'GET', `/api/oauth/organizations/${ORG}/referral/eligibility/`],
    ['an empty param segment', 'GET', '/api/oauth/organizations//referral/eligibility'],
    ['the wrong method', 'POST', `/api/oauth/organizations/${ORG}/referral/eligibility`],
    ['a literal that differs by case', 'GET', `/api/oauth/organizations/${ORG}/Referral/eligibility`],
    ['an archive name without its suffix', 'GET', '/claude-code-releases/plugins/claude-plugins-official/abc'],
    ['an archive name that is only its suffix', 'GET', '/claude-code-releases/plugins/claude-plugins-official/.zip'],
  ]
  for (const [name, method, path] of nearMisses) {
    test(`a near miss resolves to no endpoint: ${name}`, () => {
      const host = path.startsWith('/claude-code-releases/') ? 'downloads.claude.ai' : API
      expect(resolve(host, method, path)).toBeNull()
    })
  }

  test('an out-of-scope row resolves to itself, with its reason', () => {
    const endpoint = resolve(API, 'POST', '/v1/code/sessions/s1/archive')
    expect(endpoint?.id).toBe('code-session-archive')
    expect(endpoint?.scope === 'out-of-scope' && endpoint.reason).toBe(
      'remote sessions, bridge and teleport: a mock session runs locally',
    )
  })

  test('an unknown host resolves to no endpoint, even on a known path', () => {
    expect(resolve('example.com', 'POST', '/v1/messages')).toBeNull()
  })

  test('the marketplace version pointer and its archive are different rows', () => {
    const base = '/claude-code-releases/plugins/claude-plugins-official'
    expect(resolve('downloads.claude.ai', 'GET', `${base}/latest`)?.id).toBe('marketplace-version')
    expect(resolve('downloads.claude.ai', 'GET', `${base}/3d3c42e.zip`)?.id).toBe('marketplace-archive')
  })

  // PROJECT.md "What the mock generates": every endpoint it names, as a request Claude Code sends.
  const inScope: [InScopeId, string, string, string][] = [
    ['messages', API, 'POST', '/v1/messages'],
    ['messages-count-tokens', API, 'POST', '/v1/messages/count_tokens'],
    ['oauth-profile', API, 'GET', '/api/oauth/profile'],
    ['oauth-roles', API, 'GET', '/api/oauth/claude_cli/roles'],
    ['oauth-usage', API, 'GET', '/api/oauth/usage'],
    ['oauth-validate', API, 'POST', '/api/oauth/validate'],
    ['oauth-token', 'platform.claude.com', 'POST', '/v1/oauth/token'],
    ['claude-cli-bootstrap', API, 'GET', '/api/claude_cli/bootstrap'],
    ['feature-flag-eval', API, 'POST', '/api/eval/sdk-key'],
    ['event-logging-batch', API, 'POST', '/api/event_logging/batch'],
    ['event-logging-v2-batch', API, 'POST', '/api/event_logging/v2/batch'],
    ['metrics-enabled', API, 'GET', '/api/claude_code/organizations/metrics_enabled'],
    ['remote-managed-settings', API, 'GET', '/api/claude_code/settings'],
    ['policy-limits', API, 'GET', '/api/claude_code/policy_limits'],
    ['penguin-mode', API, 'GET', '/api/claude_code_penguin_mode'],
    ['grove-config', API, 'GET', '/api/claude_code_grove'],
    ['referral-eligibility', API, 'GET', `/api/oauth/organizations/${ORG}/referral/eligibility`],
    ['overage-credit-grant', API, 'GET', `/api/oauth/organizations/${ORG}/overage_credit_grant`],
    ['mcp-registry', API, 'GET', '/mcp-registry/v0/servers'],
    ['mcp-servers', API, 'GET', '/v1/mcp_servers'],
    [
      'marketplace-version',
      'downloads.claude.ai',
      'GET',
      '/claude-code-releases/plugins/claude-plugins-official/latest',
    ],
    ['changelog', 'raw.githubusercontent.com', 'GET', '/anthropics/claude-code/refs/heads/main/CHANGELOG.md'],
    ['organization-skills', API, 'GET', `/api/oauth/organizations/${ORG}/skills/list-skills`],
    ['model-selector', API, 'GET', `/api/organizations/${ORG}/model_selector/cc`],
    ['organization-marketplaces', API, 'GET', `/api/oauth/organizations/${ORG}/marketplaces`],
    ['organization-plugins', API, 'GET', `/api/oauth/organizations/${ORG}/plugins/list-plugins`],
    ['datadog-logs', 'http-intake.logs.us5.datadoghq.com', 'POST', '/api/v2/logs'],
  ]
  for (const [id, host, method, path] of inScope) {
    test(`${method} ${host}${path} resolves to in-scope ${id}`, () => {
      const endpoint = resolve(host, method, path)
      expect(endpoint?.id).toBe(id)
      expect(endpoint?.scope).toBe('in-scope')
    })
  }

  test('the in-scope rows are exactly the ones PROJECT.md names', () => {
    const declared = ENDPOINT_TABLE.endpoints.filter(endpoint => endpoint.scope === 'in-scope').map(e => e.id)
    expect(declared.toSorted()).toEqual(inScope.map(([id]) => id).toSorted())
  })
})

describe('buildEndpointTable', () => {
  const row = (id: string, path: string, method: EndpointRow['method'] = 'GET'): EndpointRow => ({
    id,
    host: API,
    method,
    path,
    scope: 'in-scope',
  })

  const rejected: [string, EndpointRow[], RegExp][] = [
    ['two brace pairs in a segment', [row('a', '/x/{a}{b}')], /endpoint a: segment \{a\}\{b\} of \/x\/\{a\}\{b\}/],
    ['an unbalanced brace', [row('a', '/x/{a')], /endpoint a: segment \{a of/],
    ['an empty param name', [row('a', '/x/{}')], /endpoint a: segment \{\} of/],
    ['a param name outside [a-z_]', [row('a', '/x/{Org}')], /endpoint a: segment \{Org\} of/],
    ['an empty segment', [row('a', '/x//y')], /endpoint a: \/x\/\/y has an empty segment/],
    ['a path without a leading slash', [row('a', 'x/y')], /endpoint a: x\/y does not start with \//],
    ['a duplicate id', [row('a', '/x'), row('a', '/y')], /duplicate endpoint id a/],
    ['a literal a param accepts', [row('a', '/x/latest'), row('b', '/x/{v}')], /endpoints a and b overlap/],
    ['two params that differ only by name', [row('a', '/x/{v}'), row('b', '/x/{w}')], /endpoints a and b overlap/],
    [
      'two params whose prefix and suffix can agree',
      [row('a', '/x/v{n}.zip'), row('b', '/x/{n}.tar.zip')],
      /endpoints a and b overlap/,
    ],
  ]
  for (const [name, rows, message] of rejected) {
    test(`rejects ${name}`, () => {
      expect(() => buildEndpointTable(rows)).toThrow(message)
    })
  }

  const accepted: [string, EndpointRow[]][] = [
    ['two rows that differ only by method', [row('a', '/x/{v}'), row('b', '/x/{v}', 'POST')]],
    ['a literal beside a param whose suffix it lacks', [row('a', '/x/latest'), row('b', '/x/{sha}.zip')]],
    ['two params whose suffixes disagree', [row('a', '/x/{n}.zip'), row('b', '/x/{n}.tar')]],
    ['two params whose prefixes disagree', [row('a', '/x/v{n}'), row('b', '/x/w{n}')]],
    ['templates of different lengths', [row('a', '/x/{v}'), row('b', '/x/{v}/y')]],
  ]
  for (const [name, rows] of accepted) {
    test(`accepts ${name}`, () => {
      expect(buildEndpointTable(rows).endpoints.map(endpoint => endpoint.id)).toEqual(rows.map(r => r.id))
    })
  }
})
