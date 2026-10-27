package servicegen_test

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
)

const api = "https://api.anthropic.com"

// send puts a request through the table and dispatch, exactly as the server does.
func send(t *testing.T, method, rawURL, body string) (dispatch.Dispatched, string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	resolution, miss := endpoint.For(account.Default()).Resolve(method, u.Host, u.EscapedPath())
	d := dispatch.Dispatch(context.Background(), resolution, miss, generator.Request{Method: method, URL: u, Body: []byte(body)})
	raw, err := io.ReadAll(d.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return d, string(raw)
}

// Each request is what Claude Code sends, as far as the answer depends on it.
func TestFixedAnswersAreTheSameOnEveryRequestAndReportNothing(t *testing.T) {
	for _, c := range []struct {
		name, method, url, body string
		status                  int
		contentType, answer     string
	}{
		{"feature-flag eval", "POST", api + "/api/eval/sdk-mock", `{"attributes":{}}`, 200, "application/json", `{"features":{}}`},
		{"event logging batch", "POST", api + "/api/event_logging/batch", `{"events":[]}`, 200, "application/json", `{}`},
		{"event logging v2 batch", "POST", api + "/api/event_logging/v2/batch", `{"events":[]}`, 200, "application/json", `{}`},
		{"Datadog log intake", "POST", "https://http-intake.logs.us5.datadoghq.com/api/v2/logs", `[]`, 200, "application/json", `{}`},
		{"Datadog error tracking", "POST", "https://browser-intake-us5-datadoghq.com/api/v2/logs?ddsource=browser&dd-evp-origin=browser", `[]`, 200, "application/json", `{}`},
		{"metrics_enabled", "GET", api + "/api/claude_code/organizations/metrics_enabled", "", 200, "application/json", `{"metrics_logging_enabled":false}`},
		{"remote managed settings, none", "GET", api + "/api/claude_code/settings", "", 204, "", ``},
		{"policy limits", "GET", api + "/api/claude_code/policy_limits", "", 200, "application/json", `{"restrictions":{}}`},
		{"penguin mode", "GET", api + "/api/claude_code_penguin_mode", "", 200, "application/json", `{"disabled_reason":null,"enabled":false}`},
		{"grove config", "GET", api + "/api/claude_code_grove", "", 200, "application/json", `{"domain_excluded":false,"grove_enabled":false,"notice_is_grace_period":true,"notice_reminder_frequency":null}`},
		{"MCP registry", "GET", api + "/mcp-registry/v0/servers?version=latest&limit=100", "", 200, "application/json", `{"metadata":{},"servers":[]}`},
		{"MCP server list", "GET", api + "/v1/mcp_servers?limit=1000", "", 200, "application/json", `{"data":[],"next_page":null}`},
		{"marketplace version pointer", "GET", "https://downloads.claude.ai/claude-code-releases/plugins/claude-plugins-official/latest", "", 200, "text/plain; charset=utf-8", strings.Repeat("0", 40) + "\n"},
		{"changelog", "GET", "https://raw.githubusercontent.com/anthropics/claude-code/refs/heads/main/CHANGELOG.md", "", 200, "text/plain; charset=utf-8", "# Changelog\n"},
	} {
		for i := 0; i < 2; i++ {
			d, body := send(t, c.method, c.url, c.body)
			if d.Verdict.Outcome != dispatch.Served || d.Response.Status != c.status || d.Response.Header.Get("Content-Type") != c.contentType || body != c.answer || len(d.Facts) != 0 {
				t.Errorf("%s (request %d): %s %d %q %q facts %v, want served %d %q %q and no facts", c.name, i+1, d.Verdict.Outcome, d.Response.Status, d.Response.Header.Get("Content-Type"), body, d.Facts, c.status, c.contentType, c.answer)
			}
		}
	}
}

func hasGenerator(e endpoint.Endpoint) bool { _, ok := e.Generator(); return ok }

func TestEveryInScopeRowHasAGenerator(t *testing.T) {
	// Only the Messages row waits on its generator (mock-mock-flu.coa.c0w).
	for _, e := range endpoint.For(account.Default()).Endpoints() {
		if e.InScope() && e.ID() != "messages" && !hasGenerator(e) {
			t.Errorf("%s has no generator", e.ID())
		}
	}
}

const count = api + "/v1/messages/count_tokens?beta=true"

func counted(t *testing.T, body string) int {
	t.Helper()
	d, raw := send(t, "POST", count, body)
	var answer struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal([]byte(raw), &answer); err != nil || d.Verdict.Outcome != dispatch.Served {
		t.Fatalf("%s: %q %v", d.Verdict.Outcome, raw, err)
	}
	if d.Facts["input_tokens"] != answer.InputTokens || len(d.Facts) != 1 {
		t.Errorf("facts = %v, want only the count %d", d.Facts, answer.InputTokens)
	}
	return answer.InputTokens
}

func request(text string) string {
	return `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":` + quote(text) + `}]}`
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestTheSameRequestCountsTheSameAndALongerOneCountsMore(t *testing.T) {
	short := counted(t, request("hello"))
	if short <= 0 || counted(t, request("hello")) != short || counted(t, request(strings.Repeat("hello ", 100))) <= short {
		t.Errorf("short counted %d", short)
	}
}

func TestACountIgnoresWhatShapesOnlyAReply(t *testing.T) {
	base := counted(t, request("hello"))
	withReplyFields := `{"model":"claude-sonnet-5-5","max_tokens":32000,"stream":true,"metadata":{"user_id":"someone"},"messages":[{"role":"user","content":"hello"}]}`
	if got := counted(t, withReplyFields); got != base {
		t.Errorf("counted %d with reply-shaping fields, %d without", got, base)
	}
}

func TestAConversationCarryingASystemRoleMessageCounts(t *testing.T) {
	hook := `{"role":"system","content":[{"type":"text","text":"SessionStart hook additional context: be brief"}]}`
	body := `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hello"},` + hook + `]}`
	if counted(t, body) <= counted(t, request("hello")) {
		t.Error("a system-role message added nothing to the count")
	}
}

func TestTheSystemPromptAndToolsCount(t *testing.T) {
	base := counted(t, `{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	for name, body := range map[string]string{
		"a system string": `{"model":"m","system":"be brief, be kind, be careful","messages":[{"role":"user","content":"hello"}]}`,
		"system blocks":   `{"model":"m","system":[{"type":"text","text":"be brief, be kind, be careful"}],"messages":[{"role":"user","content":"hello"}]}`,
		"tools":           `{"model":"m","tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`,
	} {
		if got := counted(t, body); got <= base {
			t.Errorf("%s counted %d, no more than the bare request's %d", name, got, base)
		}
	}
}

func TestABodyThatIsNotATokenCountRequestIsDeclinedSayingWhy(t *testing.T) {
	for name, c := range map[string]struct{ body, why string }{
		"not JSON":                      {"model=x", "not a JSON object"},
		"JSON then more":                {`{"model":"m","messages":[]} x`, "not a JSON object"},
		"not an object":                 {`[1]`, "body is not a token count request: not a JSON object"},
		"a stray closer":                {`{"model":"m","messages":[{"role":"user","content":"x"}]}}`, "body is not a token count request: not a JSON object"},
		"null content":                  {`{"model":"m","messages":[{"role":"user","content":null}]}`, "content"},
		"no model":                      {`{"messages":[{"role":"user","content":"x"}]}`, "body is not a token count request: model"},
		"no messages":                   {`{"model":"m","messages":[]}`, "body is not a token count request: messages"},
		"messages that are not":         {`{"model":"m","messages":[1]}`, "body is not a token count request: messages[0]"},
		"a role the API has not":        {`{"model":"m","messages":[{"role":"tool","content":"x"}]}`, "role"},
		"a text block without its text": {`{"model":"m","messages":[{"role":"user","content":[{"type":"text"}]}]}`, "must have its text"},
		"a tool_result holding one":     {`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"tool_result"}]}]}]}`, "cannot hold"},
		"a system of the wrong shape":   {`{"model":"m","system":7,"messages":[{"role":"user","content":"x"}]}`, "system"},
	} {
		d, _ := send(t, "POST", count, c.body)
		if d.Verdict.Outcome != dispatch.Unmatched || d.Response.Status != 400 || !strings.Contains(d.Verdict.Why, c.why) {
			t.Errorf("%s: %s %d %q, want a 400 declining with %q", name, d.Verdict.Outcome, d.Response.Status, d.Verdict.Why, c.why)
		}
	}
}

func TestContentBlocksOfOtherTypesPass(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Read","input":{}},{"type":"thinking","thinking":"x"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"ok"},{"type":"image","source":{}}]},{"type":"tool_result","tool_use_id":"u"}]}]}`
	if counted(t, body) <= 0 {
		t.Error("counted nothing")
	}
}
