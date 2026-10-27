package messagesgen_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/builtin"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
)

const model = "claude-sonnet-5-5"

type sent struct {
	d    dispatch.Dispatched
	body string
}

// scripts is a directory holding the given script files, name to JSON text.
func scripts(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func body(text string, stream bool) string {
	b, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 1024, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": text}}})
	return string(b)
}

// send puts a Messages request through the table and dispatch, exactly as the server does.
func send(t *testing.T, ctx context.Context, dirs []string, request string) sent {
	t.Helper()
	u, _ := url.Parse("https://api.anthropic.com/v1/messages?beta=true")
	resolution, miss := endpoint.For(account.Default(), dirs).Resolve("POST", u.Host, u.EscapedPath())
	d := dispatch.Dispatch(ctx, resolution, miss, generator.Request{Method: "POST", URL: u, Body: []byte(request)})
	raw, err := io.ReadAll(d.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return sent{d, string(raw)}
}

func reply(content, stop string) map[string]string {
	return map[string]string{"any.json": `{"reply":{"kind":"message","content":` + content + `,"stop_reason":"` + stop + `"}}`}
}

type event = map[string]any

// events reads a text/event-stream body as a client does: each event an `event:` line naming it and a
// `data:` line holding it as JSON, whose `type` is that name.
func events(t *testing.T, s sent) []event {
	t.Helper()
	if got := s.d.Response.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type %q", got)
	}
	if !strings.HasSuffix(s.body, "\n\n") {
		t.Fatalf("stream does not end on a blank line: %q", s.body)
	}
	var out []event
	for _, block := range strings.Split(strings.TrimSuffix(s.body, "\n\n"), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("malformed event %q", block)
		}
		var e event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		if "event: "+e["type"].(string) != lines[0] {
			t.Fatalf("event line %q for data type %v", lines[0], e["type"])
		}
		out = append(out, e)
	}
	return out
}

// types names each event, and each delta by its own type.
func types(stream []event) []string {
	var out []string
	for _, e := range stream {
		if e["type"] == "content_block_delta" {
			out = append(out, "delta:"+e["delta"].(event)["type"].(string))
			continue
		}
		out = append(out, e["type"].(string))
	}
	return out
}

func deltas(stream []event, field string) string {
	var joined strings.Builder
	for _, e := range stream {
		if d, ok := e["delta"].(event); ok && e["type"] == "content_block_delta" {
			if s, ok := d[field].(string); ok {
				joined.WriteString(s)
			}
		}
	}
	return joined.String()
}

// accumulate builds the message a stream describes, as the Anthropic SDK does.
func accumulate(stream []event) event {
	message := event{}
	var content []event
	partial := map[int]string{}
	for _, e := range stream {
		index, _ := e["index"].(float64)
		i := int(index)
		switch e["type"] {
		case "message_start":
			message = e["message"].(event)
		case "content_block_start":
			content = append(content, e["content_block"].(event))
		case "content_block_delta":
			d, b := e["delta"].(event), content[i]
			switch d["type"] {
			case "text_delta":
				b["text"] = b["text"].(string) + d["text"].(string)
			case "thinking_delta":
				b["thinking"] = b["thinking"].(string) + d["thinking"].(string)
			case "signature_delta":
				b["signature"] = d["signature"]
			case "input_json_delta":
				partial[i] += d["partial_json"].(string)
			}
		case "content_block_stop":
			if content[i]["type"] == "tool_use" {
				var input any
				if err := json.Unmarshal([]byte(partial[i]), &input); err != nil {
					panic(err)
				}
				content[i]["input"] = input
			}
		case "message_delta":
			for k, v := range e["delta"].(event) {
				message[k] = v
			}
			for k, v := range e["usage"].(event) {
				message["usage"].(event)[k] = v
			}
		}
	}
	asAny := make([]any, len(content))
	for i, b := range content {
		asAny[i] = b
	}
	message["content"] = asAny
	return message
}

func decode(t *testing.T, text string) event {
	t.Helper()
	var m event
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%v: %q", err, text)
	}
	return m
}

const toolInput = `{"command":"ls -la /tmp","description":"List the temporary directory"}`

func TestAStreamedTextBlockIsTheAPIsEventsInOrder(t *testing.T) {
	text := "Hello there, this text is long enough to arrive in more than one delta."
	dir := scripts(t, reply(`[{"type":"text","text":"`+text+`"}]`, "end_turn"))
	s := send(t, context.Background(), []string{dir}, body("hi", true))
	stream := events(t, s)
	n := strings.Count(strings.Join(types(stream), " "), "delta:text_delta")
	if n < 2 {
		t.Errorf("%d deltas, want more than one", n)
	}
	want := append(append([]string{"message_start", "content_block_start"}, repeat("delta:text_delta", n)...), "content_block_stop", "message_delta", "message_stop")
	if got := types(stream); !reflect.DeepEqual(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
	if got := deltas(stream, "text"); got != text {
		t.Errorf("deltas join to %q", got)
	}
	start := stream[0]["message"].(event)
	if start["type"] != "message" || start["role"] != "assistant" || start["model"] != model || start["stop_reason"] != nil || len(start["content"].([]any)) != 0 {
		t.Errorf("message_start %v", start)
	}
	if !reflect.DeepEqual(stream[1], event{"type": "content_block_start", "index": 0.0, "content_block": event{"type": "text", "text": ""}}) {
		t.Errorf("content_block_start %v", stream[1])
	}
	end := stream[len(stream)-2]
	if !reflect.DeepEqual(end["delta"], event{"stop_reason": "end_turn", "stop_sequence": nil}) || end["usage"].(event)["output_tokens"].(float64) <= 1 {
		t.Errorf("message_delta %v", end)
	}
	if s.d.Verdict.Outcome != dispatch.Served || s.d.Response.Status != 200 {
		t.Errorf("verdict %v status %d", s.d.Verdict, s.d.Response.Status)
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestAStreamedToolUseCarriesItsIDAndInputAsInputJSONDeltas(t *testing.T) {
	dir := scripts(t, reply(`[{"type":"tool_use","name":"Bash","input":`+toolInput+`}]`, "tool_use"))
	stream := events(t, send(t, context.Background(), []string{dir}, body("list it", true)))
	n := len(types(stream)) - 5
	want := append(append([]string{"message_start", "content_block_start"}, repeat("delta:input_json_delta", n)...), "content_block_stop", "message_delta", "message_stop")
	if n < 2 || !reflect.DeepEqual(types(stream), want) {
		t.Errorf("events %v", types(stream))
	}
	start := stream[1]["content_block"].(event)
	if id, _ := start["id"].(string); !regexp.MustCompile(`^toolu_[0-9a-f]{24}$`).MatchString(id) || start["name"] != "Bash" || !reflect.DeepEqual(start["input"], event{}) {
		t.Errorf("content_block_start %v", start)
	}
	if got := deltas(stream, "partial_json"); got != toolInput {
		t.Errorf("partial_json joins to %q, want %q", got, toolInput)
	}
	if stream[len(stream)-2]["delta"].(event)["stop_reason"] != "tool_use" {
		t.Errorf("message_delta %v", stream[len(stream)-2])
	}
}

func TestAStreamedThinkingBlockSendsItsSignatureInOneDeltaAfterItsText(t *testing.T) {
	thinking := "The user wants a listing; the Bash tool can do that."
	dir := scripts(t, reply(`[{"type":"thinking","thinking":"`+thinking+`"}]`, "max_tokens"))
	stream := events(t, send(t, context.Background(), []string{dir}, body("think", true)))
	got := types(stream)
	if last := got[len(got)-4]; last != "delta:signature_delta" || strings.Count(strings.Join(got, " "), "signature_delta") != 1 {
		t.Errorf("events %v", got)
	}
	if !reflect.DeepEqual(stream[1]["content_block"], event{"type": "thinking", "thinking": "", "signature": ""}) {
		t.Errorf("content_block_start %v", stream[1])
	}
	if deltas(stream, "thinking") != thinking || deltas(stream, "signature") == "" {
		t.Errorf("thinking %q signature %q", deltas(stream, "thinking"), deltas(stream, "signature"))
	}
}

func TestAStreamAccumulatesToTheMessageTheSameRequestGetsAsJSON(t *testing.T) {
	content := `[{"type":"thinking","thinking":"Listing first."},{"type":"text","text":"Let me look 👀 at the directory."},{"type":"tool_use","name":"Bash","input":` + toolInput + `},{"type":"tool_use","name":"Read","input":{}}]`
	dir := scripts(t, reply(content, "tool_use"))
	streamed := accumulate(events(t, send(t, context.Background(), []string{dir}, body("look", true))))
	j := send(t, context.Background(), []string{dir}, body("look", false))
	if got := j.d.Response.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type %q", got)
	}
	// The raw bodies differ in `stream`, so the ids do too; everything else is one message.
	whole := decode(t, j.body)
	for _, m := range []event{streamed, whole} {
		delete(m, "id")
		for _, b := range m["content"].([]any) {
			delete(b.(event), "id")
			delete(b.(event), "signature")
		}
	}
	if !reflect.DeepEqual(streamed, whole) {
		t.Errorf("streamed %v\nJSON     %v", streamed, whole)
	}
}

func TestStreamedTextSplitsByCodePoint(t *testing.T) {
	text := strings.Repeat("🙂", 40)
	dir := scripts(t, reply(`[{"type":"text","text":"`+text+`"}]`, "end_turn"))
	stream := events(t, send(t, context.Background(), []string{dir}, body("smile", true)))
	n := 0
	for _, e := range stream {
		if e["type"] == "content_block_delta" {
			n++
			if chunk := e["delta"].(event)["text"].(string); !utf8.ValidString(chunk) || utf8.RuneCountInString(chunk) > 16 {
				t.Errorf("delta %q", chunk)
			}
		}
	}
	if n != 3 || deltas(stream, "text") != text {
		t.Errorf("%d deltas", n)
	}
}

func TestAJSONMessageHasGeneratedIDsUsageAndTheRequestsModel(t *testing.T) {
	dir := scripts(t, reply(`[{"type":"text","text":"hi"},{"type":"tool_use","name":"Bash","input":`+toolInput+`}]`, "tool_use"))
	s := send(t, context.Background(), []string{dir}, body("hello", false))
	m := decode(t, s.body)
	if !regexp.MustCompile(`^msg_[0-9a-f]{24}$`).MatchString(m["id"].(string)) || m["model"] != model || m["stop_reason"] != "tool_use" || m["stop_sequence"] != nil {
		t.Errorf("message %v", m)
	}
	usage := m["usage"].(event)
	if usage["input_tokens"].(float64) < 1 || usage["output_tokens"].(float64) < 1 || usage["cache_read_input_tokens"].(float64) != 0 {
		t.Errorf("usage %v", usage)
	}
	if got := m["content"].([]any)[1].(event)["input"]; !reflect.DeepEqual(got, decode(t, toolInput)) {
		t.Errorf("tool input %v", got)
	}
	again := send(t, context.Background(), []string{dir}, body("hello", false))
	other := send(t, context.Background(), []string{dir}, body("goodbye", false))
	if again.body != s.body || decode(t, other.body)["id"] == m["id"] {
		t.Errorf("ids: same request %q vs %q, other request %v", again.body, s.body, decode(t, other.body)["id"])
	}
}

func TestTheRecordedFactsNameTheScriptAndWhatWasSent(t *testing.T) {
	dir := scripts(t, reply(`[{"type":"text","text":"hi"}]`, "end_turn"))
	m := decode(t, send(t, context.Background(), []string{dir}, body("hello", false)).body)
	s := send(t, context.Background(), []string{dir}, body("hello", false))
	want := generator.Facts{"script": filepath.Join(dir, "any.json"), "reply": generator.Facts{"kind": "json", "id": m["id"], "stop_reason": "end_turn"}}
	if !reflect.DeepEqual(s.d.Facts, want) {
		t.Errorf("facts %v, want %v", s.d.Facts, want)
	}
	st := send(t, context.Background(), []string{dir}, body("hello", true))
	if f := st.d.Facts["reply"].(generator.Facts); f["kind"] != "stream" || f["event_delay_ms"] != int64(0) || f["stop_reason"] != "end_turn" {
		t.Errorf("stream facts %v", f)
	}
}

func TestALiteral429IsSentAsWritten(t *testing.T) {
	dir := scripts(t, map[string]string{"limit.json": `{"reply":{"kind":"literal","status":429,"headers":{"retry-after":"7","content-type":"application/json"},"body":"{\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}"}}`})
	s := send(t, context.Background(), []string{dir}, body("hello", true))
	r := s.d.Response
	if r.Status != 429 || r.Header.Get("Retry-After") != "7" || r.Header.Get("Content-Type") != "application/json" || !strings.Contains(s.body, "slow down") {
		t.Errorf("%d %v %q", r.Status, r.Header, s.body)
	}
	if s.d.Verdict.Outcome != dispatch.Served || !reflect.DeepEqual(s.d.Facts["reply"], generator.Facts{"kind": "literal", "status": 429}) {
		t.Errorf("verdict %v facts %v", s.d.Verdict, s.d.Facts)
	}
}

func TestALiteralOnAStatusWithNoBodyHasAnEmptyBody(t *testing.T) {
	dir := scripts(t, map[string]string{"empty.json": `{"reply":{"kind":"literal","status":204,"body":""}}`})
	s := send(t, context.Background(), []string{dir}, body("hello", false))
	if s.d.Response.Status != 204 || s.body != "" || s.d.Response.Body == nil {
		t.Errorf("%d %q", s.d.Response.Status, s.body)
	}
}

func TestARequestNoScriptMatchesIsRefusedAndNamesEveryScriptTried(t *testing.T) {
	// The built-ins are tried after the consumer's scripts, so they are named too.
	// builtin_test.go pins the list itself; this test is about how a refusal reports whatever it is.
	builtIns := []string{}
	for _, b := range builtin.All() {
		builtIns = append(builtIns, b.Origin)
	}
	dir := scripts(t, map[string]string{"a.json": `{"match":{"lastUserContains":["nope"]},"reply":{"kind":"literal","status":200,"body":"x"}}`})
	s := send(t, context.Background(), []string{dir}, body("hello", false))
	if want := fmt.Sprintf("no script matches: %d tried", 1+len(builtIns)); s.d.Verdict.Outcome != dispatch.Unmatched || s.d.Verdict.Why != want || s.d.Response.Status != 400 {
		t.Errorf("verdict %v status %d", s.d.Verdict, s.d.Response.Status)
	}
	if !reflect.DeepEqual(s.d.Facts["scripts_tried"], append([]string{filepath.Join(dir, "a.json")}, builtIns...)) {
		t.Errorf("facts %v", s.d.Facts)
	}
	none := send(t, context.Background(), nil, body("hello", false))
	if want := fmt.Sprintf("no script matches: %d tried", len(builtIns)); none.d.Verdict.Why != want {
		t.Errorf("no scripts: %v", none.d.Verdict)
	}
}

// The built-ins are wired into the endpoint table: a quota probe is answered with no consumer scripts, and
// a consumer script that matches it answers instead.
func TestBuiltInsAnswerThroughTheTableAndAConsumerScriptWins(t *testing.T) {
	probe, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 1, "messages": []any{map[string]any{"role": "user", "content": "quota"}}})
	s := send(t, context.Background(), nil, string(probe))
	if s.d.Verdict.Outcome != dispatch.Served || s.d.Facts["script"] != "built-in: quota probe" || !strings.Contains(s.body, `"stop_reason":"max_tokens"`) {
		t.Errorf("verdict %v facts %v body %q", s.d.Verdict, s.d.Facts, s.body)
	}
	dir := scripts(t, reply(`[{"type":"text","text":"mine"}]`, "end_turn"))
	s = send(t, context.Background(), []string{dir}, string(probe))
	if s.d.Facts["script"] != filepath.Join(dir, "any.json") || !strings.Contains(s.body, "mine") {
		t.Errorf("facts %v body %q", s.d.Facts, s.body)
	}
}

func TestABadScriptFailsEveryMessagesRequestAndNamesItsPath(t *testing.T) {
	dir := scripts(t, map[string]string{"good.json": `{"reply":{"kind":"literal","status":200,"body":"x"}}`, "bad.json": `{"reply":`})
	for _, request := range []string{body("hello", false), `not json`} {
		s := send(t, context.Background(), []string{dir}, request)
		if s.d.Verdict.Outcome != dispatch.Failed || s.d.Response.Status != 500 || !strings.Contains(s.d.Verdict.Why, filepath.Join(dir, "bad.json")) {
			t.Errorf("%q: verdict %v status %d", request, s.d.Verdict, s.d.Response.Status)
		}
	}
}

func TestABodyThatIsNotAMessagesRequestIsDeclinedWithTheReason(t *testing.T) {
	dir := scripts(t, reply(`[{"type":"text","text":"hi"}]`, "end_turn"))
	s := send(t, context.Background(), []string{dir}, `{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"x"}]}`)
	if s.d.Verdict.Outcome != dispatch.Unmatched || !strings.Contains(s.d.Verdict.Why, "max_tokens") {
		t.Errorf("verdict %v", s.d.Verdict)
	}
}

func TestAStreamWaitsBetweenEventsAndStopsWhenTheRequestEnds(t *testing.T) {
	dir := scripts(t, map[string]string{"slow.json": `{"reply":{"kind":"message","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","eventDelayMs":40}}`})
	started := time.Now()
	s := send(t, context.Background(), []string{dir}, body("hello", true))
	events := len(strings.Split(strings.TrimSuffix(s.body, "\n\n"), "\n\n"))
	// The first event is not preceded by a wait; each of the others is.
	if took, min := time.Since(started), time.Duration(events-1)*40*time.Millisecond; took < min {
		t.Errorf("%d events took %v, want at least %v", events, took, min)
	}

	ctx, cancel := context.WithCancel(context.Background())
	u, _ := url.Parse("https://api.anthropic.com/v1/messages")
	resolution, miss := endpoint.For(account.Default(), []string{dir}).Resolve("POST", u.Host, u.Path)
	d := dispatch.Dispatch(ctx, resolution, miss, generator.Request{Method: "POST", URL: u, Body: []byte(body("hello", true))})
	buf := make([]byte, 1<<16)
	if n, err := d.Response.Body.Read(buf); n == 0 || err != nil {
		t.Fatalf("first read %d %v", n, err)
	}
	cancel()
	if _, err := d.Response.Body.Read(buf); err != context.Canceled {
		t.Errorf("read after cancel: %v", err)
	}
}

func TestAHeaderOnTheLiteralIsNotSharedWithTheScript(t *testing.T) {
	dir := scripts(t, map[string]string{"limit.json": `{"reply":{"kind":"literal","status":429,"headers":{"retry-after":"7"},"body":"x"}}`})
	first := send(t, context.Background(), []string{dir}, body("hello", false))
	first.d.Response.Header.Set("Retry-After", "changed")
	if got := send(t, context.Background(), []string{dir}, body("hello", false)).d.Response.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After %q", got)
	}
}

func TestIdenticalRequestsGetDistinctIDsThatAReplayReproduces(t *testing.T) {
	dirs := []string{scripts(t, reply(`[{"type":"text","text":"hi"},{"type":"tool_use","name":"x","input":{"a":1}}]`, "tool_use"))}
	ids := func() []string {
		u, _ := url.Parse("https://api.anthropic.com/v1/messages")
		table := endpoint.For(account.Default(), dirs)
		var out []string
		for range 2 {
			resolution, miss := table.Resolve("POST", u.Host, u.Path)
			d := dispatch.Dispatch(context.Background(), resolution, miss, generator.Request{Method: "POST", URL: u, Body: []byte(body("same", false))})
			raw, _ := io.ReadAll(d.Response.Body)
			m := decode(t, string(raw))
			out = append(out, m["id"].(string)+m["content"].([]any)[1].(map[string]any)["id"].(string))
		}
		return out
	}
	first, replay := ids(), ids()
	if first[0] == first[1] {
		t.Errorf("two identical requests share ids %q", first[0])
	}
	if !reflect.DeepEqual(first, replay) {
		t.Errorf("a replay of the same requests got other ids: %v then %v", first, replay)
	}
}

func TestOutputTokensNeverExceedTheRequestsMaxTokens(t *testing.T) {
	dirs := []string{scripts(t, reply(`[{"type":"text","text":"`+strings.Repeat("word ", 400)+`"}]`, "max_tokens"))}
	request := `{"model":"` + model + `","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`
	m := decode(t, send(t, context.Background(), dirs, request).body)
	if got := m["usage"].(map[string]any)["output_tokens"]; got != float64(1) {
		t.Errorf("output_tokens %v for max_tokens 1", got)
	}
}

func TestAToolInputIsTheSameBytesStreamedAndWhole(t *testing.T) {
	dirs := []string{scripts(t, reply(`[{"type":"tool_use","name":"x","input":{"cmd":"a<b && c"}}]`, "tool_use"))}
	whole := send(t, context.Background(), dirs, body("go", false)).body
	streamed := deltas(events(t, send(t, context.Background(), dirs, body("go", true))), "partial_json")
	var m struct {
		Content []struct {
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(whole), &m); err != nil {
		t.Fatal(err)
	}
	if string(m.Content[0].Input) != streamed {
		t.Errorf("whole %s, streamed %s", m.Content[0].Input, streamed)
	}
	if want := `{"cmd":"a<b && c"}`; string(m.Content[0].Input) != want {
		t.Errorf("input %s, want the script's bytes %s", m.Content[0].Input, want)
	}
}
