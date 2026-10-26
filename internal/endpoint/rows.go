package endpoint

// API is the host a request sent straight to the mock's port is addressed to (PROJECT.md "How a
// program reaches it").
const API = "api.anthropic.com"

const (
	hostAPI            = API
	hostPlatform       = "platform.claude.com"
	hostClaudeAI       = "claude.ai"
	hostDownloads      = "downloads.claude.ai"
	hostGitHubRaw      = "raw.githubusercontent.com"
	hostDatadogLogs    = "http-intake.logs.us5.datadoghq.com"
	hostDatadogBrowser = "browser-intake-us5-datadoghq.com"
	hostMCPProxy       = "mcp-proxy.anthropic.com"
)

// Reasons many rows share, one constant each.
const (
	reasonRemote       = "remote sessions, bridge and teleport: a mock session runs locally"
	reasonBrowserLogin = "browser login flow: `login` writes a config dir the mock accepts instead"
	reasonAPIKeyLogin  = "API-key login: the mock logs Claude Code in with OAuth"
	reasonConsent      = "data-sharing consent prompt"
	reasonBilling      = "billing, referral and extra-usage surfaces"
	reasonFeedback     = "feedback upload"
	reasonTriggers     = "scheduled cloud agents"
	reasonPreflight    = "`claude doctor` connectivity preflight"
	reasonConnectors   = "claude.ai connectors"
)

// [LAW:one-source-of-truth] PROJECT.md "Endpoint table": every endpoint Claude Code is known to call,
// for server dispatch, `coverage`, the CONNECT host allowlist and the leaf certificate's hosts.
var rows = []Row{
	InScope("messages", API, "POST", "/v1/messages", nil),
	InScope("messages-count-tokens", API, "POST", "/v1/messages/count_tokens", nil),
	InScope("oauth-profile", API, "GET", "/api/oauth/profile", nil),
	InScope("oauth-roles", API, "GET", "/api/oauth/claude_cli/roles", nil),
	InScope("oauth-usage", API, "GET", "/api/oauth/usage", nil),
	InScope("oauth-validate", API, "POST", "/api/oauth/validate", nil),
	InScope("oauth-token", hostPlatform, "POST", "/v1/oauth/token", nil),
	InScope("claude-cli-bootstrap", API, "GET", "/api/claude_cli/bootstrap", nil),
	InScope("feature-flag-eval", API, "POST", "/api/eval/{client_key}", nil),
	InScope("event-logging-batch", API, "POST", "/api/event_logging/batch", nil),
	InScope("event-logging-v2-batch", API, "POST", "/api/event_logging/v2/batch", nil),
	InScope("metrics-enabled", API, "GET", "/api/claude_code/organizations/metrics_enabled", nil),
	InScope("remote-managed-settings", API, "GET", "/api/claude_code/settings", nil),
	InScope("policy-limits", API, "GET", "/api/claude_code/policy_limits", nil),
	InScope("penguin-mode", API, "GET", "/api/claude_code_penguin_mode", nil),
	InScope("grove-config", API, "GET", "/api/claude_code_grove", nil),
	InScope("referral-eligibility", API, "GET", "/api/oauth/organizations/{org}/referral/eligibility", nil),
	InScope("overage-credit-grant", API, "GET", "/api/oauth/organizations/{org}/overage_credit_grant", nil),
	InScope("mcp-registry", API, "GET", "/mcp-registry/v0/servers", nil),
	InScope("mcp-servers", API, "GET", "/v1/mcp_servers", nil),
	InScope("marketplace-version", hostDownloads, "GET", "/claude-code-releases/plugins/claude-plugins-official/latest", nil),
	InScope("changelog", hostGitHubRaw, "GET", "/anthropics/claude-code/refs/heads/main/CHANGELOG.md", nil),
	InScope("organization-skills", API, "GET", "/api/oauth/organizations/{org}/skills/list-skills", nil),
	InScope("model-selector", API, "GET", "/api/organizations/{org}/model_selector/cc", nil),
	InScope("organization-marketplaces", API, "GET", "/api/oauth/organizations/{org}/marketplaces", nil),
	InScope("organization-plugins", API, "GET", "/api/oauth/organizations/{org}/plugins/list-plugins", nil),
	InScope("datadog-logs", hostDatadogLogs, "POST", "/api/v2/logs", nil),
	InScope("datadog-error-tracking", hostDatadogBrowser, "POST", "/api/v2/logs", nil),
	OutOfScope("claude-cli-profile", API, "GET", "/api/claude_cli_profile", reasonAPIKeyLogin),
	OutOfScope("oauth-authorize", hostPlatform, "GET", "/oauth/authorize", reasonBrowserLogin),
	OutOfScope("oauth-create-api-key", API, "POST", "/api/oauth/claude_cli/create_api_key", reasonBrowserLogin),
	OutOfScope("api-hello", API, "GET", "/api/hello", reasonPreflight),
	OutOfScope("oauth-hello", hostPlatform, "GET", "/v1/oauth/hello", reasonPreflight),
	OutOfScope("user-settings", API, "GET", "/api/claude_code/user_settings", "settings sync"),
	OutOfScope("user-settings-put", API, "PUT", "/api/claude_code/user_settings", "settings sync"),
	OutOfScope("team-memory", API, "GET", "/api/claude_code/team_memory", "team memory sync"),
	OutOfScope("team-memory-put", API, "PUT", "/api/claude_code/team_memory", "team memory sync"),
	OutOfScope("otel-metrics", API, "POST", "/api/claude_code/metrics", "BigQuery metrics export: metrics_enabled keeps it off"),
	OutOfScope("grove", API, "POST", "/api/claude_code_grove", reasonConsent),
	OutOfScope("account-settings", API, "GET", "/api/oauth/account/settings", reasonConsent),
	OutOfScope("account-settings-post", API, "POST", "/api/oauth/account/settings", reasonConsent),
	OutOfScope("grove-notice-viewed", API, "POST", "/api/oauth/account/grove_notice_viewed", reasonConsent),
	OutOfScope("referral-redemptions", API, "GET", "/api/oauth/organizations/{org}/referral/redemptions", reasonBilling),
	OutOfScope("admin-requests", API, "POST", "/api/oauth/organizations/{org}/admin_requests", reasonBilling),
	OutOfScope("admin-requests-me", API, "GET", "/api/oauth/organizations/{org}/admin_requests/me", reasonBilling),
	OutOfScope("admin-requests-eligibility", API, "GET", "/api/oauth/organizations/{org}/admin_requests/eligibility", reasonBilling),
	OutOfScope("first-token-date", API, "GET", "/api/organization/claude_code_first_token_date", reasonBilling),
	OutOfScope("cloud-credit", API, "GET", "/v1/code/promo/cloud_credit", reasonBilling),
	OutOfScope("mcp-start-auth", API, "POST", "/api/organizations/{org}/mcp/start-auth/{server_id}", reasonConnectors),
	OutOfScope("mcp-proxy", hostMCPProxy, "POST", "/v1/mcp/{server_id}", reasonConnectors),
	OutOfScope("mcp-proxy-get", hostMCPProxy, "GET", "/v1/mcp/{server_id}", reasonConnectors),
	OutOfScope("mcp-proxy-delete", hostMCPProxy, "DELETE", "/v1/mcp/{server_id}", reasonConnectors),
	OutOfScope("mcp-client-metadata", hostClaudeAI, "GET", "/oauth/claude-code-client-metadata", reasonConnectors),
	OutOfScope("connector-directory", API, "GET", "/api/directory/servers", reasonConnectors),
	OutOfScope("organization-skill-download", API, "GET", "/api/oauth/organizations/{org}/skills/{skill}/download", "organization skill download"),
	OutOfScope("marketplace-archive", hostDownloads, "GET", "/claude-code-releases/plugins/claude-plugins-official/{sha}.zip", "plugin marketplace archive: a binary download"),
	OutOfScope("plugin-install-counts", hostGitHubRaw, "GET", "/anthropics/claude-plugins-official/refs/heads/stats/stats/plugin-installs.json", "plugin marketplace install counts"),
	OutOfScope("frame-contract", API, "GET", "/api/frame/contract/latest", "Frame web-app publishing"),
	OutOfScope("web-domain-info", API, "GET", "/api/web/domain_info", "WebFetch tool domain check"),
	OutOfScope("cli-feedback", API, "POST", "/api/claude_cli_feedback", reasonFeedback),
	OutOfScope("shared-session-transcripts", API, "POST", "/api/claude_code_shared_session_transcripts", reasonFeedback),
	OutOfScope("file-upload", API, "POST", "/api/oauth/file_upload", "Brief tool attachment upload"),
	OutOfScope("voice-stream", API, "GET", "/api/ws/speech_to_text/voice_stream", "voice mode: a WebSocket"),
	OutOfScope("file-content", API, "GET", "/api/oauth/files/{file_id}/content", reasonRemote),
	OutOfScope("trusted-devices", API, "POST", "/api/auth/trusted_devices", reasonRemote),
	OutOfScope("code-sessions", API, "POST", "/v1/code/sessions", reasonRemote),
	OutOfScope("code-session", API, "GET", "/v1/code/sessions/{session_id}", reasonRemote),
	OutOfScope("code-session-put", API, "PUT", "/v1/code/sessions/{session_id}", reasonRemote),
	OutOfScope("code-session-archive", API, "POST", "/v1/code/sessions/{session_id}/archive", reasonRemote),
	OutOfScope("code-session-bridge", API, "POST", "/v1/code/sessions/{session_id}/bridge", reasonRemote),
	OutOfScope("code-session-worker", API, "PUT", "/v1/code/sessions/{session_id}/worker", reasonRemote),
	OutOfScope("code-session-worker-heartbeat", API, "POST", "/v1/code/sessions/{session_id}/worker/heartbeat", reasonRemote),
	OutOfScope("code-session-worker-events", API, "POST", "/v1/code/sessions/{session_id}/worker/events", reasonRemote),
	OutOfScope("code-session-worker-events-delivery", API, "POST", "/v1/code/sessions/{session_id}/worker/events/delivery", reasonRemote),
	OutOfScope("code-session-client-presence", API, "POST", "/v1/code/sessions/{session_id}/client/presence", reasonRemote),
	OutOfScope("code-session-teleport-events", API, "POST", "/v1/code/sessions/{session_id}/teleport-events", reasonRemote),
	OutOfScope("environment-work-poll", API, "GET", "/v1/environments/{env_id}/work/poll", reasonRemote),
	OutOfScope("environment-work-heartbeat", API, "POST", "/v1/environments/{env_id}/work/{session_id}/heartbeat", reasonRemote),
	OutOfScope("environment-providers", API, "GET", "/v1/environment_providers", reasonRemote),
	OutOfScope("environment-providers-cloud-create", API, "POST", "/v1/environment_providers/cloud/create", reasonRemote),
	OutOfScope("sessions", API, "POST", "/v1/sessions", reasonRemote),
	OutOfScope("sessions-list", API, "GET", "/v1/sessions", reasonRemote),
	OutOfScope("session", API, "GET", "/v1/sessions/{session_id}", reasonRemote),
	OutOfScope("session-events", API, "GET", "/v1/sessions/{session_id}/events", reasonRemote),
	OutOfScope("session-archive", API, "POST", "/v1/sessions/{session_id}/archive", reasonRemote),
	OutOfScope("github-import-token", API, "POST", "/v1/code/github/import-token", reasonRemote),
	OutOfScope("code-repos", API, "GET", "/api/oauth/organizations/{org}/code/repos/{owner}/{repo}", reasonRemote),
	OutOfScope("github-sync-auth", API, "GET", "/api/oauth/organizations/{org}/sync/github/auth", reasonRemote),
	OutOfScope("upstream-proxy-ca-cert", API, "GET", "/v1/code/upstreamproxy/ca-cert", reasonRemote),
	OutOfScope("upstream-proxy-ws", API, "GET", "/v1/code/upstreamproxy/ws", reasonRemote),
	OutOfScope("code-triggers", API, "GET", "/v1/code/triggers", reasonTriggers),
	OutOfScope("code-triggers-create", API, "POST", "/v1/code/triggers", reasonTriggers),
	OutOfScope("code-trigger", API, "GET", "/v1/code/triggers/{trigger_id}", reasonTriggers),
	OutOfScope("code-trigger-update", API, "PATCH", "/v1/code/triggers/{trigger_id}", reasonTriggers),
	OutOfScope("code-trigger-delete", API, "DELETE", "/v1/code/triggers/{trigger_id}", reasonTriggers),
	OutOfScope("code-trigger-run", API, "POST", "/v1/code/triggers/{trigger_id}/run", reasonTriggers),
}
