package endpoint

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
)

var org = account.SyntheticUUID(2)

func resolve(t *testing.T, method, rawURL string) (Resolution, bool) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return Default.Resolve(method, u)
}

func TestNearMissesResolveToNothing(t *testing.T) {
	const archives = "https://downloads.claude.ai/claude-code-releases/plugins/claude-plugins-official/"
	referral := "https://api.anthropic.com/api/oauth/organizations/" + org + "/referral"
	for name, c := range map[string]struct{ method, url string }{
		"an extra segment":                             {"GET", referral + "/eligibility/x"},
		"a missing segment":                            {"GET", referral},
		"a trailing slash":                             {"GET", referral + "/eligibility/"},
		"an empty param segment":                       {"GET", "https://api.anthropic.com/api/oauth/organizations//referral/eligibility"},
		"the wrong method":                             {"POST", referral + "/eligibility"},
		"a literal that differs by case":               {"GET", "https://api.anthropic.com/api/oauth/organizations/" + org + "/Referral/eligibility"},
		"an archive name without its suffix":           {"GET", archives + "abc"},
		"an archive name that is only its suffix":      {"GET", archives + ".zip"},
		"a param whose escapes do not decode":          {"GET", "https://api.anthropic.com/api/oauth/organizations/%E0/referral/eligibility"},
		"a literal whose escapes decode to other text": {"GET", referral + "/eligibility%2F"},
		"an unknown host on a known path":              {"POST", "https://example.com/v1/messages"},
	} {
		if r, ok := resolve(t, c.method, c.url); ok {
			t.Errorf("%s: %s %s resolved to %s", name, c.method, c.url, r.Endpoint.ID())
		}
	}
}

func TestOutOfScopeRowsResolveWithTheirReason(t *testing.T) {
	r, ok := resolve(t, "POST", "https://api.anthropic.com/v1/code/sessions/s1/archive")
	reason, out := r.Endpoint.OutOfScopeReason()
	if !ok || r.Endpoint.ID() != "code-session-archive" || !out || reason != reasonRemote {
		t.Errorf("got %v %q %v %q", ok, r.Endpoint.ID(), out, reason)
	}
	for _, method := range []string{"POST", "GET", "DELETE"} {
		r, ok := resolve(t, method, "https://mcp-proxy.anthropic.com/v1/mcp/srv")
		if reason, out := r.Endpoint.OutOfScopeReason(); !ok || !out || reason != reasonConnectors {
			t.Errorf("%s mcp-proxy: got %v %v %q", method, ok, out, reason)
		}
	}
}

// Every in-scope endpoint, as a request Claude Code sends.
var inScopeRequests = map[ID]struct{ method, url string }{
	"messages":                  {"POST", "https://api.anthropic.com/v1/messages"},
	"messages-count-tokens":     {"POST", "https://api.anthropic.com/v1/messages/count_tokens"},
	"oauth-profile":             {"GET", "https://api.anthropic.com/api/oauth/profile"},
	"oauth-roles":               {"GET", "https://api.anthropic.com/api/oauth/claude_cli/roles"},
	"oauth-usage":               {"GET", "https://api.anthropic.com/api/oauth/usage"},
	"oauth-validate":            {"POST", "https://api.anthropic.com/api/oauth/validate"},
	"oauth-token":               {"POST", "https://platform.claude.com/v1/oauth/token"},
	"claude-cli-bootstrap":      {"GET", "https://api.anthropic.com/api/claude_cli/bootstrap"},
	"feature-flag-eval":         {"POST", "https://api.anthropic.com/api/eval/sdk-key"},
	"event-logging-batch":       {"POST", "https://api.anthropic.com/api/event_logging/batch"},
	"event-logging-v2-batch":    {"POST", "https://api.anthropic.com/api/event_logging/v2/batch"},
	"metrics-enabled":           {"GET", "https://api.anthropic.com/api/claude_code/organizations/metrics_enabled"},
	"remote-managed-settings":   {"GET", "https://api.anthropic.com/api/claude_code/settings"},
	"policy-limits":             {"GET", "https://api.anthropic.com/api/claude_code/policy_limits"},
	"penguin-mode":              {"GET", "https://api.anthropic.com/api/claude_code_penguin_mode"},
	"grove-config":              {"GET", "https://api.anthropic.com/api/claude_code_grove"},
	"referral-eligibility":      {"GET", "https://api.anthropic.com/api/oauth/organizations/" + org + "/referral/eligibility"},
	"overage-credit-grant":      {"GET", "https://api.anthropic.com/api/oauth/organizations/" + org + "/overage_credit_grant"},
	"mcp-registry":              {"GET", "https://api.anthropic.com/mcp-registry/v0/servers"},
	"mcp-servers":               {"GET", "https://api.anthropic.com/v1/mcp_servers"},
	"marketplace-version":       {"GET", "https://downloads.claude.ai/claude-code-releases/plugins/claude-plugins-official/latest"},
	"changelog":                 {"GET", "https://raw.githubusercontent.com/anthropics/claude-code/refs/heads/main/CHANGELOG.md"},
	"organization-skills":       {"GET", "https://api.anthropic.com/api/oauth/organizations/" + org + "/skills/list-skills"},
	"model-selector":            {"GET", "https://api.anthropic.com/api/organizations/" + org + "/model_selector/cc"},
	"organization-marketplaces": {"GET", "https://api.anthropic.com/api/oauth/organizations/" + org + "/marketplaces"},
	"organization-plugins":      {"GET", "https://api.anthropic.com/api/oauth/organizations/" + org + "/plugins/list-plugins"},
	"datadog-logs":              {"POST", "https://http-intake.logs.us5.datadoghq.com/api/v2/logs"},
	"datadog-error-tracking":    {"POST", "https://browser-intake-us5-datadoghq.com/api/v2/logs"},
}

func TestInScopeRequestsResolveToTheirRows(t *testing.T) {
	for id, c := range inScopeRequests {
		r, ok := resolve(t, c.method, c.url)
		if !ok || r.Endpoint.ID() != id || !r.Endpoint.InScope() {
			t.Errorf("%s %s: got %v %q, want in-scope %s", c.method, c.url, ok, r.Endpoint.ID(), id)
		}
	}
	var declared []ID
	for _, e := range Default.Endpoints() {
		if e.InScope() {
			declared = append(declared, e.ID())
		}
	}
	if want := slices.Sorted(maps.Keys(inScopeRequests)); !slices.Equal(slices.Sorted(slices.Values(declared)), want) {
		t.Errorf("in-scope rows %v, want %v", declared, want)
	}
}

func TestParams(t *testing.T) {
	for _, c := range []struct {
		method, url string
		want        map[string]string
	}{
		{"GET", inScopeRequests["referral-eligibility"].url, map[string]string{"org": org}},
		{"GET", "https://downloads.claude.ai/claude-code-releases/plugins/claude-plugins-official/3d3c42e.zip", map[string]string{"sha": "3d3c42e"}},
		{"POST", "https://api.anthropic.com/v1/messages", map[string]string{}},
		{"GET", "https://api.anthropic.com/api/oauth/organizations/a%20b%2Fc/referral/eligibility", map[string]string{"org": "a b/c"}},
	} {
		r, ok := resolve(t, c.method, c.url)
		if !ok || !maps.Equal(r.Params, c.want) {
			t.Errorf("%s: got %v %v, want %v", c.url, ok, r.Params, c.want)
		}
	}
}

func TestRequestFormsThatDoNotChangeTheRow(t *testing.T) {
	for _, raw := range []string{
		"https://api.anthropic.com/v1/%6Dessages",
		"https://API.Anthropic.com:443/v1/messages?beta=true",
	} {
		if r, ok := resolve(t, "POST", raw); !ok || r.Endpoint.ID() != "messages" {
			t.Errorf("%s: got %v %q", raw, ok, r.Endpoint.ID())
		}
	}
	if r, ok := resolve(t, "GET", "https://downloads.claude.ai/claude-code-releases/plugins/claude-plugins-official/latest"); !ok || r.Endpoint.ID() != "marketplace-version" {
		t.Errorf("pointer: got %q", r.Endpoint.ID())
	}
}

func TestHostsAreTheEightCoveredHosts(t *testing.T) {
	want := []string{hostAPI, hostPlatform, hostDownloads, hostGitHubRaw, hostDatadogLogs, hostDatadogBrowser, hostMCPProxy, hostClaudeAI}
	if got := Default.Hosts(); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Errorf("hosts %v", got)
	}
}

func get(id ID, path string) Row { return inScope(id, hostAPI, "GET", path) }

func TestBuildRejects(t *testing.T) {
	for name, c := range map[string]struct {
		rows []Row
		want string
	}{
		"two brace pairs in a segment":                  {[]Row{get("a", "/x/{a}{b}")}, `endpoint a: /x/\{a\}\{b\}: segment \{a\}\{b\} is not`},
		"an unbalanced brace":                           {[]Row{get("a", "/x/{a")}, `segment \{a is not`},
		"an empty param name":                           {[]Row{get("a", "/x/{}")}, `segment \{\} is not`},
		"a param name outside [a-z_]":                   {[]Row{get("a", "/x/{Org}")}, `segment \{Org\} is not`},
		"an empty segment":                              {[]Row{get("a", "/x//y")}, `endpoint a: /x//y: empty segment`},
		"a path without a leading slash":                {[]Row{get("a", "x/y")}, `endpoint a: x/y does not start with /`},
		"a host with capitals":                          {[]Row{inScope("a", "API.anthropic.com", "GET", "/x")}, `host "API.anthropic.com" is not`},
		"a host with a port":                            {[]Row{inScope("a", hostAPI+":443", "GET", "/x")}, `host "api.anthropic.com:443" is not`},
		"an unknown method":                             {[]Row{inScope("a", hostAPI, "get", "/x")}, `method "get" is not`},
		"a dot segment":                                 {[]Row{get("a", "/x/../y")}, `segment \.\. is not what a request for it is matched as`},
		"a literal with a query":                        {[]Row{get("a", "/x?y")}, `segment x\?y is not`},
		"a literal written percent-encoded":             {[]Row{get("a", "/x%20y")}, `segment x%20y is not`},
		"fixed text with a fragment":                    {[]Row{get("a", "/x/{v}#y")}, `segment \{v\}#y is not`},
		"a param name used twice":                       {[]Row{get("a", "/x/{v}/y/{v}")}, `/x/\{v\}/y/\{v\} names \{v\} twice`},
		"a blank out-of-scope reason":                   {[]Row{outOfScope("a", hostAPI, "GET", "/x", " ")}, `endpoint a: an out-of-scope row needs a reason`},
		"a duplicate id, once however often it repeats": {[]Row{get("a", "/x"), get("a", "/y"), get("a", "/z")}, `^endpoint table:\nduplicate endpoint id a$`},
		"a literal a param accepts":                     {[]Row{get("a", "/x/latest"), get("b", "/x/{v}")}, `endpoints a and b overlap`},
		"two params that differ by name":                {[]Row{get("a", "/x/{v}"), get("b", "/x/{w}")}, `endpoints a and b overlap`},
		"params whose fixed text can agree":             {[]Row{get("a", "/x/v{n}.zip"), get("b", "/x/{n}.tar.zip")}, `endpoints a and b overlap`},
	} {
		_, err := Build(c.rows)
		if err == nil || !regexp.MustCompile(c.want).MatchString(err.Error()) {
			t.Errorf("%s: got %v, want /%s/", name, err, c.want)
		}
	}
}

func TestBuildAccepts(t *testing.T) {
	for name, rows := range map[string][]Row{
		"rows that differ only by method":                 {get("a", "/x/{v}"), inScope("b", hostAPI, "POST", "/x/{v}")},
		"a literal beside a param it lacks the suffix of": {get("a", "/x/latest"), get("b", "/x/{sha}.zip")},
		"params whose suffixes disagree":                  {get("a", "/x/{n}.zip"), get("b", "/x/{n}.tar")},
		"params whose prefixes disagree":                  {get("a", "/x/v{n}"), get("b", "/x/w{n}")},
		"templates of different lengths":                  {get("a", "/x/{v}"), get("b", "/x/{v}/y")},
		"a literal with a space, sent as %20":             {get("a", "/x y")},
	} {
		if _, err := Build(rows); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	tbl, err := Build([]Row{get("a", "/x y")})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := tbl.Resolve("GET", &url.URL{Host: hostAPI, Path: "/x y"}); !ok || r.Endpoint.ID() != "a" {
		t.Errorf("a literal with a space did not resolve")
	}
}
