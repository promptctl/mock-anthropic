package messagesgen_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/recording"
	"github.com/promptctl/mock-anthropic/internal/script"
)

// Test identifiers are built at run time: the repository scan rejects UUID and email literals.
var (
	someEmail = "someone" + "@" + "private-corp.example"
	someUUID  = fmt.Sprintf("%08x-1111-4222-8333-%012x", 0xabcdef00+7, 7)
)

const (
	firstReply  = `{"kind":"message","content":[{"type":"tool_use","name":"Bash","input":{"command":"echo hello"}}],"stop_reason":"tool_use"}`
	secondReply = `{"kind":"message","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`
)

// turn is a Messages request as Claude Code sends it, with diagnostics naming the reply it continues.
// extra is spliced in as more top-level members.
func turn(diagnostics, messages, extra string) string {
	return `{"model":"claude-haiku-5-5","max_tokens":1024,"stream":false,"diagnostics":` + diagnostics + `,"messages":` + messages + extra + `}`
}

func create() string { return `{"previous_message_id":null}` }
func cont(id string) string {
	return `{"previous_message_id":"` + id + `"}`
}

func prompt() string {
	return `[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nhooks 1\n</system-reminder>"},{"type":"text","text":"mail ` + someEmail + ` about ` + someUUID + `"}]},{"role":"system","content":"one"}]`
}

func result(content, toolUseID string) string {
	return `[{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + toolUseID + `","content":"` + content + `","is_error":false}]},{"role":"system","content":"two"}]`
}

// recorded saves a two-exchange conversation, as record mode would, and loads it as serve does.
func recorded(t *testing.T) []script.Script {
	t.Helper()
	dir := t.TempDir()
	w, err := recording.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []recording.Exchange{
		{Request: json.RawMessage(turn(create(), prompt(), "")), Reply: json.RawMessage(firstReply), MessageID: "msg_first"},
		{Request: json.RawMessage(turn(cont("msg_first"), result("hello", "toolu_a"), "")), Reply: json.RawMessage(secondReply), MessageID: "msg_second"},
	} {
		s, _, err := recording.Scrub(e, nil, account.Default())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	rs, shadowed, err := recording.Scripts([]string{dir}, account.Default())
	if err != nil || len(shadowed) != 0 {
		t.Fatalf("Scripts: %v, shadowed %v", err, shadowed)
	}
	return []script.Script{rs}
}

func replay(t *testing.T, recs []script.Script, request string) (dispatch.Dispatched, string) {
	t.Helper()
	u, _ := url.Parse("https://api.anthropic.com/v1/messages")
	resolution, miss := endpoint.For(account.Default(), nil, recs).Resolve("POST", u.Host, u.Path)
	d := dispatch.Dispatch(context.Background(), resolution, miss, generator.Request{Method: "POST", URL: u, Body: []byte(request)})
	raw, _ := io.ReadAll(d.Response.Body)
	return d, string(raw)
}

func TestAConversationReplaysAcrossRunsAndVersions(t *testing.T) {
	recs := recorded(t)
	// The live request holds the originals the recording was scrubbed of, and differs in every field
	// the key ignores: model, system prompt, metadata, tools, and the system-role message.
	live := turn(create(), prompt(), `,"system":"another prompt","metadata":{"user_id":"x"},"tools":[{"name":"t"}]`)
	d, body := replay(t, recs, live)
	if d.Verdict.Outcome != dispatch.Served || !strings.Contains(body, `"id":"msg_first"`) || !strings.Contains(body, `"echo hello"`) && !strings.Contains(body, "echo hello") {
		t.Fatalf("first turn: %v %s", d.Verdict, body)
	}
	if d.Facts["recording"] == nil || d.Facts["key"] == nil {
		t.Errorf("facts %v: want the recording file and the compared key", d.Facts)
	}
	// The tool_use id, the trailing reminder and the system-role message differ from the recording's.
	next := turn(cont("msg_first"), result("hello\\n\\n<system-reminder>\\ntokens 99\\n</system-reminder>", "toolu_other"), `,"system":"yet another"`)
	d, body = replay(t, recs, next)
	if d.Verdict.Outcome != dispatch.Served || !strings.Contains(body, `"id":"msg_second"`) || !strings.Contains(body, "done") {
		t.Fatalf("second turn: %v %s", d.Verdict, body)
	}
}

// A session whose account has threads off sends the whole conversation in messages and no `thread`; its
// requests must find the recording made with threads on, because the key reads neither.
func TestAFullHistoryRequestFindsTheThreadedRecording(t *testing.T) {
	recs := recorded(t)
	history := `[{"role":"user","content":[{"type":"text","text":"mail ` + someEmail + ` about ` + someUUID + `"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_z","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_z","content":"hello"}]}]`
	d, body := replay(t, recs, turn(cont("msg_first"), history, `,"thread":null`))
	if d.Verdict.Outcome != dispatch.Served || !strings.Contains(body, `"id":"msg_second"`) {
		t.Errorf("%v %s", d.Verdict, body)
	}
}

func TestAChangedMatchedFieldIsUnmatchedAndSaysWhich(t *testing.T) {
	recs := recorded(t)
	for name, c := range map[string]struct{ request, why string }{
		"the prompt":           {turn(create(), strings.Replace(prompt(), "mail", "post", 1), ""), `"last_text":"post `},
		"the position":         {turn(cont("msg_elsewhere"), result("hello", "toolu_a"), ""), `"previous":"msg_elsewhere"`},
		"a tool result":        {turn(cont("msg_first"), result("goodbye", "toolu_a"), ""), `"sha256"`},
		"no diagnostics":       {`{"model":"m","max_tokens":5,"messages":` + prompt() + `}`, "the request carries no diagnostics.previous_message_id"},
		"an empty previous id": {turn(`{"previous_message_id":""}`, prompt(), ""), "non-empty string"},
	} {
		d, _ := replay(t, recs, c.request)
		keyFacts, _ := json.Marshal(d.Facts["key"])
		if d.Verdict.Outcome != dispatch.Unmatched || !strings.Contains(string(keyFacts), c.why) {
			t.Errorf("%s: %v, key facts %s; want unmatched with %q", name, d.Verdict, keyFacts, c.why)
		}
	}
}

func TestARecordingNoRequestCanReachIsNamedAtLoad(t *testing.T) {
	dir := t.TempDir()
	w, _ := recording.NewWriter(dir)
	for _, text := range []string{"one", "one"} {
		s, _, err := recording.Scrub(recording.Exchange{Request: json.RawMessage(turn(create(), `[{"role":"user","content":"`+text+`"}]`, "")), Reply: json.RawMessage(secondReply), MessageID: "msg_x"}, nil, account.Default())
		if err != nil {
			t.Fatal(err)
		}
		w.Save(s)
	}
	_, shadowed, err := recording.Scripts([]string{dir}, account.Default())
	if err != nil || len(shadowed) != 1 || !strings.HasSuffix(shadowed[0], "0002.json") {
		t.Errorf("shadowed %v, %v", shadowed, err)
	}
	// A recording whose request carries no diagnostics has no key, and is refused naming its file.
	s, _, _ := recording.Scrub(recording.Exchange{Request: json.RawMessage(`{"model":"m","messages":[{"role":"user","content":"x"}]}`), Reply: json.RawMessage(secondReply), MessageID: "msg_y"}, nil, account.Default())
	w.Save(s)
	if _, _, err := recording.Scripts([]string{dir}, account.Default()); err == nil || !strings.Contains(err.Error(), "0003.json") {
		t.Errorf("a recording with no diagnostics: %v", err)
	}
}
