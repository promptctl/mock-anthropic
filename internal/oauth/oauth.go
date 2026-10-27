// Package oauth is the mock's OAuth credential: what `login` writes into a config directory and the
// token endpoint hands back on a refresh. [LAW:one-source-of-truth] One home, so a refreshed login is
// the login the mock wrote.
package oauth

import "time"

// Scopes are what a claude.ai login grants, as the token endpoint's space-separated `scope`. Claude
// Code 2.1.289 saves a refreshed token only when `user:inference` is among them, and skips the
// organization marketplaces call without `user:plugins`.
var Scopes = []string{
	"user:file_upload",
	"user:inference",
	"user:mcp_servers",
	"user:plugins",
	"user:profile",
	"user:sessions:claude_code",
}

const (
	AccessToken  = "mock-anthropic-access-token"
	RefreshToken = "mock-anthropic-refresh-token"
)

// TokenLifetime is a year, so no test outlives the mock's token.
const TokenLifetime = 365 * 24 * time.Hour
