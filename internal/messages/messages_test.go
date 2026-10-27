package messages

import (
	"strings"
	"testing"
)

func TestTokensCountsUTF16UnitsAtFourToAToken(t *testing.T) {
	for text, want := range map[string]int{"": 0, "abcd": 1, "abcde": 2, "😀😀": 1, "😀😀a": 2} {
		if got := Tokens(text); got != want {
			t.Errorf("Tokens(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestParseCountTokensRefusesWhatTheAPIRefusesAndSaysWhy(t *testing.T) {
	for body, why := range map[string]string{
		`{"model":"m","messages":[{"role":"user","content":"x"}]}}`: "body is not JSON",
		`{"model":"m","messages":[{"role":"user","content":null}]}`: "content",
		`null`: "expected an object, got null",
		`[1]`:  "expected an object, got an array",
	} {
		_, err := ParseCountTokens([]byte(body))
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: err %v, want one containing %q", body, err, why)
		}
	}
}

func body(messages string, rest string) []byte {
	return []byte(`{"model":"claude-sonnet-5-5","max_tokens":1024,"messages":` + messages + rest + `}`)
}

func TestParseRequestReadsTheTextsAScriptMatches(t *testing.T) {
	r, err := ParseRequest(body(`[
		{"role":"user","content":"run the tests"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_abc","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_abc","content":[{"type":"text","text":"12 pass"},{"type":"image"}]},{"type":"text","text":"ok"},{"type":"image","source":"x"}]},
		{"role":"system","content":"hook context"}
	]`, `,"system":[{"type":"text","text":"You are"},{"type":"text","text":"Claude"}],"stream":true`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.LastUserTexts, "|"); got != "12 pass|ok" {
		t.Errorf("LastUserTexts %q: ids, types and the system-role message are never matchable", got)
	}
	if got := strings.Join(r.SystemTexts, "|"); got != "You are|Claude" {
		t.Errorf("SystemTexts %q", got)
	}
	if !r.Stream || r.MaxTokens != 1024 || r.Model != "claude-sonnet-5-5" || r.InputTokens < 1 || !strings.Contains(r.Raw, "hook context") {
		t.Errorf("%+v", r)
	}
}

func TestParseRequestDefaultsAndRefusals(t *testing.T) {
	if r, err := ParseRequest(body(`[{"role":"user","content":"hi"}]`, `,"system":null`)); err != nil || len(r.SystemTexts) != 0 {
		t.Errorf("a null system is a missing one: %+v, %v", r, err)
	}
	if r, err := ParseRequest(body(`[{"role":"user","content":"hi"}]`, ``)); err != nil || r.Stream || len(r.SystemTexts) != 0 {
		t.Errorf("%+v, %v", r, err)
	}
	for what, c := range map[string]struct{ raw, why string }{
		"no user message":    {string(body(`[{"role":"assistant","content":"x"}]`, ``)), "has no user message"},
		"only system":        {string(body(`[{"role":"system","content":"x"}]`, ``)), "has no user message"},
		"no max_tokens":      {`{"model":"m","messages":[{"role":"user","content":"x"}]}`, "max_tokens"},
		"zero max_tokens":    {`{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"x"}]}`, "max_tokens"},
		"fractional":         {`{"model":"m","max_tokens":1.5,"messages":[{"role":"user","content":"x"}]}`, "max_tokens"},
		"stream not boolean": {string(body(`[{"role":"user","content":"x"}]`, `,"stream":"yes"`)), "stream"},
		"stream null":        {string(body(`[{"role":"user","content":"x"}]`, `,"stream":null`)), "stream"},
		"no model":           {`{"max_tokens":1,"messages":[{"role":"user","content":"x"}]}`, "model"},
	} {
		_, err := ParseRequest([]byte(c.raw))
		if err == nil || !strings.Contains(err.Error(), c.why) || !strings.HasPrefix(err.Error(), "body is not a Messages request: ") {
			t.Errorf("%s: %v, want a refusal naming %q", what, err, c.why)
		}
	}
}

func TestAToolResultWithNullContentHasNoText(t *testing.T) {
	r, err := ParseRequest(body(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":null}]}]`, ``))
	if err != nil || len(r.LastUserTexts) != 0 {
		t.Errorf("%q, %v", r.LastUserTexts, err)
	}
}
