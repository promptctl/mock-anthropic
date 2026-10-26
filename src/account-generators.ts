import { z } from 'zod'
import type { Account } from './account.ts'
import type { Answer, Facts, Generator, Generators, Json } from './dispatch.ts'
import type { Row } from './endpoints.ts'
import { parseJsonBody } from './json-body.ts'
import { ACCESS_TOKEN, OAUTH_SCOPES, REFRESH_TOKEN, TOKEN_LIFETIME_SECONDS } from './oauth.ts'

// PROJECT.md "Every other in-scope endpoint": the OAuth account endpoints, the CLI bootstrap and the
// organization-scoped paths. [LAW:one-source-of-truth] Every identity in their bodies is the account's.
// Each body is the least both Claude Code builds in use accept: 2.1.88 (its source) and 2.1.289 (its
// shipped binary). Fields neither reads are left out.

const answer = (body: Json, facts: Facts): Answer => ({ kind: 'response', response: Response.json(body), facts })

// [LAW:nothing-unseen] The identity an answer was built from, for its record.
const identity = (account: Account): Facts => ({ account_uuid: account.accountUuid, organization_uuid: account.organizationUuid })

// Claude Code reads a subscription from the organization's type: `claude_max` is `max`, and so on.
const organizationType = (account: Account) => `claude_${account.subscriptionType}`

// A fixed instant, so the same account gets the same answer on every run. 2.1.289 fetches the profile
// again on every start while the account's or the subscription's creation time is missing.
const CREATED_AT = '2026-01-01T00:00:00Z'

// A usage bucket with nothing used and no reset scheduled. A body with no bucket at all is, to 2.1.289,
// an error carried in a 200.
const UNUSED = { utilization: 0, resets_at: null }

// [LAW:single-enforcer] The one rule for organization-scoped paths: the `{org}` a request names is the
// account's organization. [LAW:no-silent-failure] Any other is a consumer misconfigured, declined
// naming both, never answered as if it were the account's.
function forOrganization(account: Account, org: string, body: Json): Answer {
  return org === account.organizationUuid
    ? answer(body, identity(account))
    : {
        kind: 'declined',
        why: `organization ${org} is not the account's organization ${account.organizationUuid}`,
        facts: { ...identity(account), organization_requested: org },
      }
}

// [LAW:types-are-the-program] The in-scope rows whose path names an organization. They are given
// bodies, never generators, so no answer to one can skip forOrganization.
type OrganizationRow = Extract<Row, { scope: 'in-scope'; path: `${string}{org}${string}` }>
type OrganizationBodies = { readonly [Id in OrganizationRow['id']]?: Json }

export function organizationGenerators(account: Account): Generators<OrganizationRow> {
  const bodies: OrganizationBodies = {
    'organization-skills': { skills: [] },
    // No `cc` selector config, so 2.1.289 caches no model catalog and offers its own models.
    'model-selector': { organization_uuid: account.organizationUuid },
    'organization-marketplaces': { marketplaces: [] },
    // 2.1.289 asks for the next page while `has_more` is true.
    'organization-plugins': { plugins: [], has_more: false },
  }
  const generators = Object.entries(bodies).map(([id, body]) => {
    const generator: Generator<OrganizationRow> = ({ params }) => forOrganization(account, params.org, body)
    return [id, generator]
  })
  // Every key is an OrganizationRow id, which Object.fromEntries cannot carry; this cast is the one
  // place that says so. The generator itself is checked against every organization row's params.
  return Object.fromEntries(generators) as Generators<OrganizationRow>
}

// What a token request asks for, read loosely so that a refusal can say what was asked.
const TokenRequest = z.object({ grant_type: z.string(), refresh_token: z.string().optional() })

// Whose refresh token a request carried, never the token itself: a refused one may be a real login's.
type RefreshTokenKind = 'mock' | 'other' | 'none'
const refreshTokenKind = (token: string | undefined): RefreshTokenKind =>
  token === undefined ? 'none' : token === REFRESH_TOKEN ? 'mock' : 'other'

// A refresh of the login `login` writes is answered. Another grant is the browser login flow. Another
// refresh token is a login the mock did not write: answering it would overwrite that login's stored
// credential with the mock's.
function refusal(grant_type: string, token: RefreshTokenKind): string | null {
  if (grant_type !== 'refresh_token') return `grant_type ${grant_type}: the browser login flow is out of scope`
  switch (token) {
    case 'mock':
      return null
    case 'other':
      return "the refresh token is not the mock's: a login the mock did not write reached it"
    case 'none':
      return 'a refresh_token grant with no refresh_token'
  }
}

// The rows answered here: neither an organization-scoped row nor Messages can be given a generator in
// this map, so none can skip forOrganization or stand in for the Messages generator.
type AccountRow = Exclude<Row, OrganizationRow | Extract<Row, { id: 'messages' }>>

export function accountGenerators(account: Account) {
  return {
    'oauth-profile': () =>
      answer(
        {
          account: { uuid: account.accountUuid, email: account.emailAddress, created_at: CREATED_AT },
          organization: {
            uuid: account.organizationUuid,
            name: account.organizationName,
            organization_type: organizationType(account),
            rate_limit_tier: null,
            billing_type: 'stripe_subscription',
            subscription_created_at: CREATED_AT,
            has_extra_usage_enabled: false,
            cc_onboarding_flags: {},
          },
        },
        identity(account),
      ),
    'oauth-roles': () =>
      answer(
        {
          organization_uuid: account.organizationUuid,
          organization_name: account.organizationName,
          organization_role: 'admin',
          workspace_role: null,
        },
        identity(account),
      ),
    // The usage of the account's plan, which names no one.
    'oauth-usage': () => answer({ five_hour: UNUSED, seven_day: UNUSED }, {}),
    'oauth-validate': () => answer({ account_uuid: account.accountUuid, organization_uuid: account.organizationUuid }, identity(account)),
    'oauth-token': (_, request) => {
      const parsed = parseJsonBody(request.body, TokenRequest, 'an OAuth token request')
      if (parsed.kind === 'invalid') return { kind: 'declined', why: parsed.why, facts: {} }
      const { grant_type, refresh_token } = parsed.value
      // [LAW:nothing-unseen] What was asked, for the record whether answered or refused.
      const asked = { grant_type, refresh_token: refreshTokenKind(refresh_token) }
      const why = refusal(grant_type, asked.refresh_token)
      if (why !== null) return { kind: 'declined', why, facts: asked }
      return answer(
        {
          token_type: 'Bearer',
          access_token: ACCESS_TOKEN,
          refresh_token: REFRESH_TOKEN,
          expires_in: TOKEN_LIFETIME_SECONDS,
          scope: OAUTH_SCOPES.join(' '),
          account: { uuid: account.accountUuid, email_address: account.emailAddress },
          organization: { uuid: account.organizationUuid, name: account.organizationName },
        },
        { ...identity(account), ...asked },
      )
    },
    'claude-cli-bootstrap': () =>
      answer(
        {
          client_data: {},
          additional_model_options: [],
          oauth_account: {
            account_uuid: account.accountUuid,
            account_email: account.emailAddress,
            organization_uuid: account.organizationUuid,
            organization_name: account.organizationName,
            organization_type: organizationType(account),
            organization_rate_limit_tier: null,
            user_rate_limit_tier: null,
            seat_tier: null,
          },
        },
        identity(account),
      ),
  } satisfies Generators<AccountRow>
}
