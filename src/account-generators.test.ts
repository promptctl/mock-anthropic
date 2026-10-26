import { describe, expect, test } from 'bun:test'
import { DEFAULT_ACCOUNT, parseAccount } from './account.ts'
import type { Facts } from './dispatch.ts'
import type { Method } from './endpoints.ts'
import { mockGenerators } from './generators.ts'
import { send as sendTo } from './testing.ts'

// Every identity below is the override's, written out, so a body that kept the default account, or
// derived a field from the wrong one, fails.
const ACCOUNT_UUID = '00000000-0000-4000-8000-000000000065'
const ORG_UUID = '00000000-0000-4000-8000-000000000066'
const OVERRIDE = parseAccount(
  {
    accountUuid: ACCOUNT_UUID,
    emailAddress: 'override@mock-anthropic.invalid',
    organizationUuid: ORG_UUID,
    organizationName: 'Override Organization',
    subscriptionType: 'pro',
  },
  'test override',
)

const API = 'https://api.anthropic.com'
const IDENTITY = { account_uuid: ACCOUNT_UUID, organization_uuid: ORG_UUID }
const REFRESH = JSON.stringify({ grant_type: 'refresh_token', refresh_token: 'mock-anthropic-refresh-token', client_id: 'x', scope: 'user:inference' })

const send = (method: Method, url: string, body = '') => sendTo(mockGenerators({ account: OVERRIDE, scenarioDirs: [] }), method, url, body)

const ANSWERS: readonly { name: string; method: Method; url: string; body?: string; answer: unknown; facts: Facts }[] = [
  {
    name: 'OAuth profile',
    method: 'GET',
    url: `${API}/api/oauth/profile`,
    answer: {
      account: { uuid: ACCOUNT_UUID, email: 'override@mock-anthropic.invalid', created_at: '2026-01-01T00:00:00Z' },
      organization: {
        uuid: ORG_UUID,
        name: 'Override Organization',
        organization_type: 'claude_pro',
        rate_limit_tier: null,
        billing_type: 'stripe_subscription',
        subscription_created_at: '2026-01-01T00:00:00Z',
        has_extra_usage_enabled: false,
        cc_onboarding_flags: {},
      },
    },
    facts: IDENTITY,
  },
  {
    name: 'OAuth roles',
    method: 'GET',
    url: `${API}/api/oauth/claude_cli/roles`,
    answer: { organization_uuid: ORG_UUID, organization_name: 'Override Organization', organization_role: 'admin', workspace_role: null },
    facts: IDENTITY,
  },
  {
    name: 'OAuth usage, which names no one',
    method: 'GET',
    url: `${API}/api/oauth/usage`,
    answer: { five_hour: { utilization: 0, resets_at: null }, seven_day: { utilization: 0, resets_at: null } },
    facts: {},
  },
  {
    name: 'OAuth validate',
    method: 'POST',
    url: `${API}/api/oauth/validate`,
    body: 'null',
    answer: { account_uuid: ACCOUNT_UUID, organization_uuid: ORG_UUID },
    facts: IDENTITY,
  },
  {
    name: 'OAuth token refresh',
    method: 'POST',
    url: 'https://platform.claude.com/v1/oauth/token',
    body: REFRESH,
    answer: {
      token_type: 'Bearer',
      access_token: 'mock-anthropic-access-token',
      refresh_token: 'mock-anthropic-refresh-token',
      expires_in: 31536000,
      scope: 'user:file_upload user:inference user:mcp_servers user:plugins user:profile user:sessions:claude_code',
      account: { uuid: ACCOUNT_UUID, email_address: 'override@mock-anthropic.invalid' },
      organization: { uuid: ORG_UUID, name: 'Override Organization' },
    },
    facts: { ...IDENTITY, grant_type: 'refresh_token', refresh_token: 'mock' },
  },
  {
    name: 'CLI bootstrap',
    method: 'GET',
    url: `${API}/api/claude_cli/bootstrap?entrypoint=cli&model=claude-sonnet-5-5`,
    answer: {
      client_data: {},
      additional_model_options: [],
      oauth_account: {
        account_uuid: ACCOUNT_UUID,
        account_email: 'override@mock-anthropic.invalid',
        organization_uuid: ORG_UUID,
        organization_name: 'Override Organization',
        organization_type: 'claude_pro',
        organization_rate_limit_tier: null,
        user_rate_limit_tier: null,
        seat_tier: null,
      },
    },
    facts: IDENTITY,
  },
  {
    name: 'organization skills',
    method: 'GET',
    url: `${API}/api/oauth/organizations/${ORG_UUID}/skills/list-skills?include_wiggle_skills=true&entrypoint=cli`,
    answer: { skills: [] },
    facts: IDENTITY,
  },
  {
    name: 'model selector',
    method: 'GET',
    url: `${API}/api/organizations/${ORG_UUID}/model_selector/cc`,
    answer: { organization_uuid: ORG_UUID },
    facts: IDENTITY,
  },
  {
    name: 'organization marketplaces',
    method: 'GET',
    url: `${API}/api/oauth/organizations/${ORG_UUID}/marketplaces`,
    answer: { marketplaces: [] },
    facts: IDENTITY,
  },
  {
    name: 'organization plugins, one page',
    method: 'GET',
    url: `${API}/api/oauth/organizations/${ORG_UUID}/plugins/list-plugins?enabled_only=true&compact=true&limit=100&offset=0`,
    answer: { plugins: [], has_more: false },
    facts: IDENTITY,
  },
]

describe('the account generators', () => {
  for (const { name, method, url, body, answer, facts } of ANSWERS) {
    test(`${name}: answers from the overridden account, and its facts name it`, async () => {
      const dispatched = await send(method, url, body)
      expect(dispatched.verdict).toEqual({ outcome: 'served' })
      expect(dispatched.response.status).toBe(200)
      expect(dispatched.response.headers.get('content-type')).toStartWith('application/json')
      expect(await dispatched.response.json()).toEqual(answer)
      expect(dispatched.facts).toEqual(facts)
    })
  }

  test('a subscription becomes the organization type Claude Code reads it from', async () => {
    const max = mockGenerators({ account: DEFAULT_ACCOUNT, scenarioDirs: [] })
    const profile = (await (await sendTo(max, 'GET', `${API}/api/oauth/profile`)).response.json()) as { organization: { organization_type: string } }
    expect(profile.organization.organization_type).toBe('claude_max')
  })
})

describe('an organization-scoped path naming another organization', () => {
  const OTHER = DEFAULT_ACCOUNT.organizationUuid
  for (const path of [
    `/api/oauth/organizations/${OTHER}/skills/list-skills`,
    `/api/organizations/${OTHER}/model_selector/cc`,
    `/api/oauth/organizations/${OTHER}/marketplaces`,
    `/api/oauth/organizations/${OTHER}/plugins/list-plugins`,
  ]) {
    test(`${path} is declined, naming both organizations`, async () => {
      const { verdict, response, facts } = await send('GET', `${API}${path}`)
      const why = `organization ${OTHER} is not the account's organization ${ORG_UUID}`
      expect(verdict).toEqual({ outcome: 'unmatched', why })
      expect(response.status).toBe(400)
      expect(facts).toEqual({ ...IDENTITY, organization_requested: OTHER })
    })
  }
})

describe('a token request that is not a refresh of the mock login', () => {
  const token = (body: string) => send('POST', 'https://platform.claude.com/v1/oauth/token', body)

  test('another grant is declined as the browser login flow, and its facts name the grant', async () => {
    const { verdict, response, facts } = await token(JSON.stringify({ grant_type: 'authorization_code', code: 'x' }))
    expect(response.status).toBe(400)
    expect(verdict).toEqual({ outcome: 'unmatched', why: 'grant_type authorization_code: the browser login flow is out of scope' })
    expect(facts).toEqual({ grant_type: 'authorization_code', refresh_token: 'none' })
  })

  // A real login that reached the mock would have its stored credential replaced by the mock's, so it
  // is refused, and its token stays out of the record.
  test('another refresh token is declined, never exchanged for the mock token or recorded', async () => {
    const { verdict, response, facts } = await token(JSON.stringify({ grant_type: 'refresh_token', refresh_token: 'a-real-refresh-token' }))
    expect(response.status).toBe(400)
    expect(verdict).toEqual({ outcome: 'unmatched', why: "the refresh token is not the mock's: a login the mock did not write reached it" })
    expect(facts).toEqual({ grant_type: 'refresh_token', refresh_token: 'other' })
    expect(await response.text()).not.toContain('a-real-refresh-token')
  })

  // A refresh with no token is a broken request, not a real login reaching the mock, and says so.
  test('a refresh with no refresh token is declined as one', async () => {
    const { verdict, response, facts } = await token(JSON.stringify({ grant_type: 'refresh_token' }))
    expect(response.status).toBe(400)
    expect(verdict).toEqual({ outcome: 'unmatched', why: 'a refresh_token grant with no refresh_token' })
    expect(facts).toEqual({ grant_type: 'refresh_token', refresh_token: 'none' })
  })

  test('a body that is not JSON is declined, saying so', async () => {
    const { verdict, facts } = await token('grant_type=refresh_token')
    expect(verdict).toMatchObject({ outcome: 'unmatched', why: expect.stringMatching(/^body is not JSON/) })
    expect(facts).toEqual({})
  })
})
