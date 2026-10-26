import { z } from 'zod'

// PROJECT.md "Inputs / The account": the identity the mock logs Claude Code in as. Every response
// that carries identity is derived from one value of this type.

// [LAW:one-source-of-truth] The one declaration of what a synthetic UUID looks like. Synthetic UUIDs
// are built by syntheticUuid and recognized by isSyntheticUuid, so telling one from a real UUID needs
// this form, not a hand-kept list. The prefix fixes 74 of a random v4 UUID's 122 random bits, so a
// real UUID matches it with probability 2^-74.
const SYNTHETIC_UUID_PREFIX = '00000000-0000-4000-8000-'
const SYNTHETIC_UUID_DIGITS = 12
const SYNTHETIC_UUID = new RegExp(`^${SYNTHETIC_UUID_PREFIX}[0-9a-f]{${SYNTHETIC_UUID_DIGITS}}$`)

export function syntheticUuid(n: number): string {
  if (!Number.isSafeInteger(n) || n < 0 || n >= 16 ** SYNTHETIC_UUID_DIGITS) {
    throw new RangeError(`syntheticUuid: ${n} is not an integer in [0, 16^${SYNTHETIC_UUID_DIGITS})`)
  }
  return SYNTHETIC_UUID_PREFIX + n.toString(16).padStart(SYNTHETIC_UUID_DIGITS, '0')
}

export function isSyntheticUuid(text: string): boolean {
  return SYNTHETIC_UUID.test(text)
}

// [LAW:domain-language] The five facts PROJECT.md names, under the names Claude Code gives them in
// its `oauthAccount` config block and its credential, so `login` and the generators copy them across
// unrenamed. Strict, so a misspelled key in an override is rejected by name instead of ignored.
// [LAW:parse-dont-validate] Branded and readonly: an Account exists only as this schema's output,
// frozen, so no literal can pose as one and no consumer can change the one shared identity value.
const AccountSchema = z
  .strictObject({
  accountUuid: z.uuid(),
  emailAddress: z.email(),
  organizationUuid: z.uuid(),
  organizationName: z.string().regex(/\S/, 'must not be blank'),
  subscriptionType: z.enum(['pro', 'max', 'team', 'enterprise']),
  })
  .brand<'Account'>()
  .readonly()

export type Account = z.infer<typeof AccountSchema>

// Every way an account input can fail (unreadable, not JSON, schema), so a command maps a bad
// `--account FILE` to exit 2 by type.
export class AccountInputError extends Error {
  constructor(origin: string, reason: string, options?: ErrorOptions) {
    super(`account from ${origin}: ${reason}`, options)
    this.name = 'AccountInputError'
  }
}

// [LAW:parse-dont-validate] [LAW:single-enforcer] The one checkpoint an account crosses. `origin`
// says where the value came from, so the error names both the source and every bad field.
export function parseAccount(value: unknown, origin: string): Account {
  const result = AccountSchema.safeParse(value)
  if (!result.success) {
    throw new AccountInputError(origin, z.prettifyError(result.error))
  }
  return result.data
}

export const DEFAULT_ACCOUNT: Account = parseAccount(
  {
    accountUuid: syntheticUuid(1),
    emailAddress: 'user@mock-anthropic.invalid',
    organizationUuid: syntheticUuid(2),
    organizationName: 'Mock Organization',
    subscriptionType: 'max',
  },
  'DEFAULT_ACCOUNT',
)

// [LAW:effects-at-boundaries] The consumer override: a JSON file holding a complete account, which
// replaces the default whole.
export async function readAccountFile(path: string): Promise<Account> {
  const text = await Bun.file(path).text().catch((error: unknown) => {
    throw new AccountInputError(path, `cannot read: ${String(error)}`, { cause: error })
  })
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch (error) {
    throw new AccountInputError(path, `not JSON: ${String(error)}`, { cause: error })
  }
  return parseAccount(value, path)
}
