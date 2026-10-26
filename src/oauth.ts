// The mock's OAuth credential: what `login` writes into a config dir and the token endpoint hands out
// on refresh. [LAW:one-source-of-truth] One home, so a refreshed login is the login the mock wrote.

// The scopes a claude.ai login grants, as the token endpoint's space-separated `scope`. 2.1.289 saves a
// refreshed token only when `user:inference` is among them, and skips the organization marketplaces
// call without `user:plugins`.
export const OAUTH_SCOPES = [
  'user:file_upload',
  'user:inference',
  'user:mcp_servers',
  'user:plugins',
  'user:profile',
  'user:sessions:claude_code',
] as const

export const ACCESS_TOKEN = 'mock-anthropic-access-token'
export const REFRESH_TOKEN = 'mock-anthropic-refresh-token'

// A year, so no test outlives the mock's token.
export const TOKEN_LIFETIME_SECONDS = 365 * 24 * 60 * 60
