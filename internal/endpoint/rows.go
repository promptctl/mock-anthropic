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
	inScope("messages", API, "POST", "/v1/messages"),
	inScope("messages-count-tokens", API, "POST", "/v1/messages/count_tokens"),
	inScope("oauth-profile", API, "GET", "/api/oauth/profile"),
	inScope("oauth-roles", API, "GET", "/api/oauth/claude_cli/roles"),
	inScope("oauth-usage", API, "GET", "/api/oauth/usage"),
	inScope("oauth-validate", API, "POST", "/api/oauth/validate"),
	inScope("oauth-token", hostPlatform, "POST", "/v1/oauth/token"),
	inScope("claude-cli-bootstrap", API, "GET", "/api/claude_cli/bootstrap"),
	inScope("feature-flag-eval", API, "POST", "/api/eval/{client_key}"),
	inScope("event-logging-batch", API, "POST", "/api/event_logging/batch"),
	inScope("event-logging-v2-batch", API, "POST", "/api/event_logging/v2/batch"),
	inScope("metrics-enabled", API, "GET", "/api/claude_code/organizations/metrics_enabled"),
	inScope("remote-managed-settings", API, "GET", "/api/claude_code/settings"),
	inScope("policy-limits", API, "GET", "/api/claude_code/policy_limits"),
	inScope("penguin-mode", API, "GET", "/api/claude_code_penguin_mode"),
	inScope("grove-config", API, "GET", "/api/claude_code_grove"),
	inScope("referral-eligibility", API, "GET", "/api/oauth/organizations/{org}/referral/eligibility"),
	inScope("overage-credit-grant", API, "GET", "/api/oauth/organizations/{org}/overage_credit_grant"),
	inScope("mcp-registry", API, "GET", "/mcp-registry/v0/servers"),
	inScope("mcp-servers", API, "GET", "/v1/mcp_servers"),
	inScope("marketplace-version", hostDownloads, "GET", "/claude-code-releases/plugins/claude-plugins-official/latest"),
	inScope("changelog", hostGitHubRaw, "GET", "/anthropics/claude-code/refs/heads/main/CHANGELOG.md"),
	inScope("organization-skills", API, "GET", "/api/oauth/organizations/{org}/skills/list-skills"),
	inScope("model-selector", API, "GET", "/api/organizations/{org}/model_selector/cc"),
	inScope("organization-marketplaces", API, "GET", "/api/oauth/organizations/{org}/marketplaces"),
	inScope("organization-plugins", API, "GET", "/api/oauth/organizations/{org}/plugins/list-plugins"),
	inScope("datadog-logs", hostDatadogLogs, "POST", "/api/v2/logs"),
	inScope("datadog-error-tracking", hostDatadogBrowser, "POST", "/api/v2/logs"),
	outOfScope("claude-cli-profile", API, "GET", "/api/claude_cli_profile", reasonAPIKeyLogin),
	outOfScope("oauth-authorize", hostPlatform, "GET", "/oauth/authorize", reasonBrowserLogin),
	outOfScope("oauth-create-api-key", API, "POST", "/api/oauth/claude_cli/create_api_key", reasonBrowserLogin),
	outOfScope("api-hello", API, "GET", "/api/hello", reasonPreflight),
	outOfScope("oauth-hello", hostPlatform, "GET", "/v1/oauth/hello", reasonPreflight),
	outOfScope("user-settings", API, "GET", "/api/claude_code/user_settings", "settings sync"),
	outOfScope("user-settings-put", API, "PUT", "/api/claude_code/user_settings", "settings sync"),
	outOfScope("team-memory", API, "GET", "/api/claude_code/team_memory", "team memory sync"),
	outOfScope("team-memory-put", API, "PUT", "/api/claude_code/team_memory", "team memory sync"),
	outOfScope("otel-metrics", API, "POST", "/api/claude_code/metrics", "BigQuery metrics export: metrics_enabled keeps it off"),
	outOfScope("grove", API, "POST", "/api/claude_code_grove", reasonConsent),
	outOfScope("account-settings", API, "GET", "/api/oauth/account/settings", reasonConsent),
	outOfScope("account-settings-post", API, "POST", "/api/oauth/account/settings", reasonConsent),
	outOfScope("grove-notice-viewed", API, "POST", "/api/oauth/account/grove_notice_viewed", reasonConsent),
	outOfScope("referral-redemptions", API, "GET", "/api/oauth/organizations/{org}/referral/redemptions", reasonBilling),
	outOfScope("admin-requests", API, "POST", "/api/oauth/organizations/{org}/admin_requests", reasonBilling),
	outOfScope("admin-requests-me", API, "GET", "/api/oauth/organizations/{org}/admin_requests/me", reasonBilling),
	outOfScope("admin-requests-eligibility", API, "GET", "/api/oauth/organizations/{org}/admin_requests/eligibility", reasonBilling),
	outOfScope("first-token-date", API, "GET", "/api/organization/claude_code_first_token_date", reasonBilling),
	outOfScope("cloud-credit", API, "GET", "/v1/code/promo/cloud_credit", reasonBilling),
	outOfScope("mcp-start-auth", API, "POST", "/api/organizations/{org}/mcp/start-auth/{server_id}", reasonConnectors),
	outOfScope("mcp-proxy", hostMCPProxy, "POST", "/v1/mcp/{server_id}", reasonConnectors),
	outOfScope("mcp-proxy-get", hostMCPProxy, "GET", "/v1/mcp/{server_id}", reasonConnectors),
	outOfScope("mcp-proxy-delete", hostMCPProxy, "DELETE", "/v1/mcp/{server_id}", reasonConnectors),
	outOfScope("mcp-client-metadata", hostClaudeAI, "GET", "/oauth/claude-code-client-metadata", reasonConnectors),
	outOfScope("connector-directory", API, "GET", "/api/directory/servers", reasonConnectors),
	outOfScope("organization-skill-download", API, "GET", "/api/oauth/organizations/{org}/skills/{skill}/download", "organization skill download"),
	outOfScope("marketplace-archive", hostDownloads, "GET", "/claude-code-releases/plugins/claude-plugins-official/{sha}.zip", "plugin marketplace archive: a binary download"),
	outOfScope("plugin-install-counts", hostGitHubRaw, "GET", "/anthropics/claude-plugins-official/refs/heads/stats/stats/plugin-installs.json", "plugin marketplace install counts"),
	outOfScope("frame-contract", API, "GET", "/api/frame/contract/latest", "Frame web-app publishing"),
	outOfScope("web-domain-info", API, "GET", "/api/web/domain_info", "WebFetch tool domain check"),
	outOfScope("cli-feedback", API, "POST", "/api/claude_cli_feedback", reasonFeedback),
	outOfScope("shared-session-transcripts", API, "POST", "/api/claude_code_shared_session_transcripts", reasonFeedback),
	outOfScope("file-upload", API, "POST", "/api/oauth/file_upload", "Brief tool attachment upload"),
	outOfScope("voice-stream", API, "GET", "/api/ws/speech_to_text/voice_stream", "voice mode: a WebSocket"),
	outOfScope("file-content", API, "GET", "/api/oauth/files/{file_id}/content", reasonRemote),
	outOfScope("trusted-devices", API, "POST", "/api/auth/trusted_devices", reasonRemote),
	outOfScope("code-sessions", API, "POST", "/v1/code/sessions", reasonRemote),
	outOfScope("code-session", API, "GET", "/v1/code/sessions/{session_id}", reasonRemote),
	outOfScope("code-session-put", API, "PUT", "/v1/code/sessions/{session_id}", reasonRemote),
	outOfScope("code-session-archive", API, "POST", "/v1/code/sessions/{session_id}/archive", reasonRemote),
	outOfScope("code-session-bridge", API, "POST", "/v1/code/sessions/{session_id}/bridge", reasonRemote),
	outOfScope("code-session-worker", API, "PUT", "/v1/code/sessions/{session_id}/worker", reasonRemote),
	outOfScope("code-session-worker-heartbeat", API, "POST", "/v1/code/sessions/{session_id}/worker/heartbeat", reasonRemote),
	outOfScope("code-session-worker-events", API, "POST", "/v1/code/sessions/{session_id}/worker/events", reasonRemote),
	outOfScope("code-session-worker-events-delivery", API, "POST", "/v1/code/sessions/{session_id}/worker/events/delivery", reasonRemote),
	outOfScope("code-session-client-presence", API, "POST", "/v1/code/sessions/{session_id}/client/presence", reasonRemote),
	outOfScope("code-session-teleport-events", API, "POST", "/v1/code/sessions/{session_id}/teleport-events", reasonRemote),
	outOfScope("environment-work-poll", API, "GET", "/v1/environments/{env_id}/work/poll", reasonRemote),
	outOfScope("environment-work-heartbeat", API, "POST", "/v1/environments/{env_id}/work/{session_id}/heartbeat", reasonRemote),
	outOfScope("environment-providers", API, "GET", "/v1/environment_providers", reasonRemote),
	outOfScope("environment-providers-cloud-create", API, "POST", "/v1/environment_providers/cloud/create", reasonRemote),
	outOfScope("sessions", API, "POST", "/v1/sessions", reasonRemote),
	outOfScope("sessions-list", API, "GET", "/v1/sessions", reasonRemote),
	outOfScope("session", API, "GET", "/v1/sessions/{session_id}", reasonRemote),
	outOfScope("session-events", API, "GET", "/v1/sessions/{session_id}/events", reasonRemote),
	outOfScope("session-archive", API, "POST", "/v1/sessions/{session_id}/archive", reasonRemote),
	outOfScope("github-import-token", API, "POST", "/v1/code/github/import-token", reasonRemote),
	outOfScope("code-repos", API, "GET", "/api/oauth/organizations/{org}/code/repos/{owner}/{repo}", reasonRemote),
	outOfScope("github-sync-auth", API, "GET", "/api/oauth/organizations/{org}/sync/github/auth", reasonRemote),
	outOfScope("upstream-proxy-ca-cert", API, "GET", "/v1/code/upstreamproxy/ca-cert", reasonRemote),
	outOfScope("upstream-proxy-ws", API, "GET", "/v1/code/upstreamproxy/ws", reasonRemote),
	outOfScope("code-triggers", API, "GET", "/v1/code/triggers", reasonTriggers),
	outOfScope("code-triggers-create", API, "POST", "/v1/code/triggers", reasonTriggers),
	outOfScope("code-trigger", API, "GET", "/v1/code/triggers/{trigger_id}", reasonTriggers),
	outOfScope("code-trigger-update", API, "PATCH", "/v1/code/triggers/{trigger_id}", reasonTriggers),
	outOfScope("code-trigger-delete", API, "DELETE", "/v1/code/triggers/{trigger_id}", reasonTriggers),
	outOfScope("code-trigger-run", API, "POST", "/v1/code/triggers/{trigger_id}/run", reasonTriggers),
}
