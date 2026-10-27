package script

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/messages"
)

func request(t *testing.T, messagesJSON string) messages.Request {
	t.Helper()
	r, err := messages.ParseRequest([]byte(`{"model":"claude-sonnet-5-5","max_tokens":1024,"messages":` + messagesJSON + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hello(t *testing.T) messages.Request { return request(t, `[{"role":"user","content":"hello"}]`) }

// dir is a fresh scripts directory holding the given files: a string is written as is, anything else as JSON.
func dir(t *testing.T, files map[string]any) string {
	t.Helper()
	d := t.TempDir()
	for name, content := range files {
		write(t, d, name, content)
	}
	return d
}

func write(t *testing.T, d, name string, content any) string {
	t.Helper()
	text, ok := content.(string)
	if !ok {
		b, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		text = string(b)
	}
	path := filepath.Join(d, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type m = map[string]any

func says(text string, match m) m {
	file := m{"reply": m{"kind": "message", "content": []m{{"type": "text", "text": text}}, "stop_reason": "end_turn"}}
	if match != nil {
		file["match"] = match
	}
	return file
}

func selectReply(t *testing.T, dirs []string, builtIns []Script, r messages.Request) Selection {
	t.Helper()
	scripts, err := InOrder(dirs, builtIns)
	if err != nil {
		t.Fatal(err)
	}
	return FirstReply(scripts, r)
}

func textOf(t *testing.T, s Selection) string {
	t.Helper()
	matched, ok := s.(Matched)
	if !ok {
		t.Fatalf("expected a match, got %+v", s)
	}
	reply, ok := matched.Reply.(MessageReply)
	if !ok {
		t.Fatalf("expected a message reply, got %+v", matched.Reply)
	}
	return reply.Content[0].(Text).Text
}

func TestOrder(t *testing.T) {
	t.Run("within a directory, files are tried in byte order of their names", func(t *testing.T) {
		d := dir(t, m{"b.json": says("b", nil), "a.json": says("a", nil), "Z.json": says("Z", nil)})
		if got := textOf(t, selectReply(t, []string{d}, nil, hello(t))); got != "Z" {
			t.Errorf("answered %q, want Z (uppercase sorts before lowercase)", got)
		}
	})
	t.Run("directories are tried in the order given", func(t *testing.T) {
		first, second := dir(t, m{"z.json": says("first", nil)}), dir(t, m{"a.json": says("second", nil)})
		if got := textOf(t, selectReply(t, []string{first, second}, nil, hello(t))); got != "first" {
			t.Errorf("answered %q", got)
		}
	})
	t.Run("a consumer script comes before a built-in, which answers when none matches", func(t *testing.T) {
		builtIn := Script{Origin: "built-in: everything", Answer: func(messages.Request) Reply {
			return MessageReply{Content: []Block{Text{"built-in"}}, StopReason: StopEndTurn}
		}}
		d := dir(t, m{"a.json": says("consumer", m{"bodyContains": []string{"hello"}})})
		if got := textOf(t, selectReply(t, []string{d}, []Script{builtIn}, hello(t))); got != "consumer" {
			t.Errorf("answered %q", got)
		}
		other := request(t, `[{"role":"user","content":"bye"}]`)
		if got := textOf(t, selectReply(t, []string{d}, []Script{builtIn}, other)); got != "built-in" {
			t.Errorf("answered %q", got)
		}
	})
	t.Run("only .json entries that are not hidden are scripts", func(t *testing.T) {
		d := dir(t, m{"a.json.tmp": says("tmp", nil), ".b.json": says("hidden", nil), "._c.json": says("meta", nil), "notes.txt": "x", "d.json": says("d", nil)})
		scripts, err := Read([]string{d})
		if err != nil || len(scripts) != 1 || scripts[0].Origin != filepath.Join(d, "d.json") {
			t.Errorf("%v, %v", scripts, err)
		}
	})
}

func TestAScriptWrittenOrRenamedIntoPlaceAnswersTheNextRequest(t *testing.T) {
	d := t.TempDir()
	if _, ok := selectReply(t, []string{d}, nil, hello(t)).(Unmatched); !ok {
		t.Fatal("an empty directory answered")
	}
	// A consumer writes under another name and renames it into place, so it is never read half-written.
	tmp := write(t, d, "late.json.tmp", says("late", nil))
	if _, ok := selectReply(t, []string{d}, nil, hello(t)).(Unmatched); !ok {
		t.Fatal("a .tmp file answered")
	}
	if err := os.Rename(tmp, filepath.Join(d, "late.json")); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t, selectReply(t, []string{d}, nil, hello(t))); got != "late" {
		t.Errorf("answered %q", got)
	}
	write(t, d, "late.json", says("changed", nil))
	if got := textOf(t, selectReply(t, []string{d}, nil, hello(t))); got != "changed" {
		t.Errorf("answered %q", got)
	}
}

func TestMatching(t *testing.T) {
	earlier := request(t, `[
		{"role":"user","content":"run the tests"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_abc","name":"Bash","input":{"command":"go test"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_abc","content":[{"type":"text","text":"12 pass"}]}]}
	]`)
	t.Run("lastUserContains reads the last user message only; bodyContains reads the whole body", func(t *testing.T) {
		d := dir(t, m{"a.json": says("earlier turn", m{"lastUserContains": []string{"run the tests"}}), "b.json": says("anywhere", m{"bodyContains": []string{"run the tests"}})})
		if got := textOf(t, selectReply(t, []string{d}, nil, earlier)); got != "anywhere" {
			t.Errorf("answered %q", got)
		}
	})
	t.Run("lastUserContains matches a tool result, and not its id or block type", func(t *testing.T) {
		d := dir(t, m{
			"a.json": says("id", m{"lastUserContains": []string{"toolu_abc"}}),
			"b.json": says("type", m{"lastUserContains": []string{"tool_result"}}),
			"c.json": says("result", m{"lastUserContains": []string{"12 pass"}}),
		})
		if got := textOf(t, selectReply(t, []string{d}, nil, earlier)); got != "result" {
			t.Errorf("answered %q", got)
		}
	})
	t.Run("a system-role message is reached only through the raw body", func(t *testing.T) {
		r := request(t, `[{"role":"user","content":"hi"},{"role":"system","content":"hook context"}]`)
		d := dir(t, m{"a.json": says("last user", m{"lastUserContains": []string{"hook context"}}), "b.json": says("raw", m{"bodyContains": []string{"hook context"}})})
		if got := textOf(t, selectReply(t, []string{d}, nil, r)); got != "raw" {
			t.Errorf("answered %q", got)
		}
	})
	t.Run("a lastUserContains needle must fall within one text, not across two", func(t *testing.T) {
		two := request(t, `[{"role":"user","content":[{"type":"text","text":"build ok"},{"type":"text","text":"deploy"}]}]`)
		d := dir(t, m{"a.json": says("across", m{"lastUserContains": []string{"ok\ndeploy"}}), "b.json": says("each", m{"lastUserContains": []string{"ok", "deploy"}})})
		if got := textOf(t, selectReply(t, []string{d}, nil, two)); got != "each" {
			t.Errorf("answered %q", got)
		}
	})
	t.Run("every needle must appear", func(t *testing.T) {
		d := dir(t, m{"a.json": says("both", m{"bodyContains": []string{"Bash", "missing"}})})
		want := Unmatched{Tried: []string{filepath.Join(d, "a.json")}}
		if got := selectReply(t, []string{d}, nil, earlier); !reflect.DeepEqual(got, want) {
			t.Errorf("%+v, want %+v", got, want)
		}
	})
	t.Run("a script with no conditions answers every request", func(t *testing.T) {
		d := dir(t, m{"a.json": says("always", nil)})
		for _, r := range []messages.Request{earlier, hello(t)} {
			if got := textOf(t, selectReply(t, []string{d}, nil, r)); got != "always" {
				t.Errorf("answered %q", got)
			}
		}
	})
	t.Run("no match reports every script tried, in order", func(t *testing.T) {
		d := dir(t, m{"b.json": says("b", m{"bodyContains": []string{"nope"}}), "a.json": says("a", m{"bodyContains": []string{"nope"}})})
		never := Script{Origin: "built-in: never", Answer: func(messages.Request) Reply { return nil }}
		want := Unmatched{Tried: []string{filepath.Join(d, "a.json"), filepath.Join(d, "b.json"), "built-in: never"}}
		if got := selectReply(t, []string{d}, []Script{never}, hello(t)); !reflect.DeepEqual(got, want) {
			t.Errorf("%+v, want %+v", got, want)
		}
	})
}

func TestTheReply(t *testing.T) {
	d := dir(t, m{
		"a.json": m{"match": m{"lastUserContains": []string{"tool"}}, "reply": m{"kind": "message", "stop_reason": "tool_use", "content": []m{
			{"type": "thinking", "thinking": "hm"}, {"type": "tool_use", "name": "Bash", "input": m{"command": "ls"}}}}},
		"b.json": m{"reply": m{"kind": "literal", "status": 429, "headers": m{"retry-after": "1"}, "body": `{"type":"error"}`}},
		"c.json": m{"reply": m{"kind": "literal", "status": 204, "body": ""}},
	})
	use := request(t, `[{"role":"user","content":"use a tool"}]`)
	want := MessageReply{StopReason: StopToolUse, Content: []Block{Thinking{"hm"}, ToolUse{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}}}
	if got := selectReply(t, []string{d}, nil, use).(Matched).Reply; !reflect.DeepEqual(got, want) {
		t.Errorf("%+v, want %+v", got, want)
	}
	literal := LiteralReply{Status: 429, Header: http.Header{"Retry-After": {"1"}}, Body: []byte(`{"type":"error"}`)}
	if got := selectReply(t, []string{d}, nil, hello(t)).(Matched).Reply; !reflect.DeepEqual(got, literal) {
		t.Errorf("%+v, want %+v", got, literal)
	}
	scripts, err := Read([]string{d})
	if err != nil {
		t.Fatal(err)
	}
	if got := scripts[2].Answer(hello(t)).(LiteralReply); got.Body != nil || got.Status != 204 {
		t.Errorf("a 204 carries a body: %+v", got)
	}
}

func TestMaxTokensMayCutOffAReplyWithOrWithoutAToolCall(t *testing.T) {
	d := dir(t, m{
		"a.json": m{"reply": m{"kind": "message", "content": []m{{"type": "text", "text": "cut"}}, "stop_reason": "max_tokens"}},
		"b.json": m{"reply": m{"kind": "message", "content": []m{{"type": "tool_use", "name": "Bash", "input": m{}}}, "stop_reason": "max_tokens"}},
	})
	if scripts, err := Read([]string{d}); err != nil || len(scripts) != 2 {
		t.Errorf("%v, %v", scripts, err)
	}
}

func TestADelayIsKept(t *testing.T) {
	s := says("x", nil)
	s["reply"].(m)["eventDelayMs"] = 250
	scripts, err := Read([]string{dir(t, m{"a.json": s})})
	if err != nil || scripts[0].Answer(hello(t)).(MessageReply).EventDelay.Milliseconds() != 250 {
		t.Errorf("%v, %v", scripts, err)
	}
}

// A script that cannot be read fails loudly with its path: each malformed script, and the words of the
// reason its error gives after the path.
func TestAMalformedScriptFailsNamingItsPath(t *testing.T) {
	with := func(base m, extra m) m {
		for k, v := range extra {
			base[k] = v
		}
		return base
	}
	msg := func(extra m) m {
		return with(m{"kind": "message", "content": []m{{"type": "text", "text": "x"}}, "stop_reason": "end_turn"}, extra)
	}
	lit := func(extra m) m { return with(m{"kind": "literal", "status": 429, "body": ""}, extra) }
	tool := func(input any, name string) m {
		return msg(m{"stop_reason": "tool_use", "content": []m{{"type": "tool_use", "name": name, "input": input}}})
	}
	bad := map[string]struct {
		script any
		reason string
	}{
		"not JSON":                         {`{"reply":`, "not JSON"},
		"not an object":                    {`[]`, "expected an object"},
		"an unknown key":                   {m{"reply": msg(nil), "extra": true}, `Unrecognized key: "extra"`},
		"a misspelled match key":           {m{"match": m{"lastUser": []string{"x"}}, "reply": msg(nil)}, `Unrecognized key: "lastUser"`},
		"a null match":                     {m{"match": nil, "reply": msg(nil)}, "match: expected an object"},
		"no reply":                         {m{}, "reply: required"},
		"an unknown reply kind":            {m{"reply": m{"kind": "stream", "body": "x"}}, "reply: kind"},
		"a status on a message":            {m{"reply": msg(m{"status": 200})}, `Unrecognized key: "status"`},
		"a delay on a literal":             {m{"reply": lit(m{"eventDelayMs": 5})}, `Unrecognized key: "eventDelayMs"`},
		"an empty needle":                  {m{"match": m{"bodyContains": []string{""}}, "reply": msg(nil)}, "match: bodyContains: [0]"},
		"a needle that is not a string":    {m{"match": m{"bodyContains": []any{1}}, "reply": msg(nil)}, "match: bodyContains: [0]"},
		"no content":                       {m{"reply": msg(m{"content": []m{}})}, "reply: content"},
		"a block of an unknown type":       {m{"reply": msg(m{"content": []m{{"type": "image"}}})}, "reply: content[0]: type"},
		"a text block with no text":        {m{"reply": msg(m{"content": []m{{"type": "text"}}})}, "reply: content[0]: text"},
		"a tool call that ends the turn":   {m{"reply": with(tool(m{}, "Bash"), m{"stop_reason": "end_turn"})}, "reply: stop_reason"},
		"tool_use with no tool call":       {m{"reply": msg(m{"stop_reason": "tool_use"})}, "reply: stop_reason"},
		"an unknown stop reason":           {m{"reply": msg(m{"stop_reason": "stop"})}, "reply: stop_reason"},
		"a status Response refuses":        {m{"reply": lit(m{"status": 101})}, "reply: status"},
		"a status with a fraction":         {m{"reply": lit(m{"status": 429.5})}, "reply: status"},
		"a header name HTTP refuses":       {m{"reply": lit(m{"headers": m{"retry after": "1"}})}, "Invalid header name"},
		"a header value HTTP refuses":      {m{"reply": lit(m{"headers": m{"retry-after": "1\n2"}})}, "invalid value"},
		"a delay no timer holds":           {m{"reply": msg(m{"eventDelayMs": 1 << 31})}, "reply: eventDelayMs"},
		"a negative delay":                 {m{"reply": msg(m{"eventDelayMs": -1})}, "reply: eventDelayMs"},
		"one header named twice":           {m{"reply": lit(m{"headers": m{"Retry-After": "1", "retry-after": "30"}})}, "names a field twice: retry-after"},
		"a body on a 204":                  {m{"reply": lit(m{"status": 204, "body": "x"})}, "reply: body"},
		"no body":                          {m{"reply": m{"kind": "literal", "status": 500}}, "reply: body: required"},
		"tool input that is not an object": {m{"reply": tool("ls", "Bash")}, "reply: content[0]: input"},
		"a tool call with no name":         {m{"reply": tool(m{}, "")}, "reply: content[0]: name"},
		"a tool name the API refuses":      {m{"reply": tool(m{}, "my tool")}, "reply: content[0]: name"},
		"a framing header":                 {m{"reply": lit(m{"headers": m{"Content-Length": "5"}})}, "the mock frames the response itself"},
		"the first of two bad keys":        {m{"reply": msg(m{"b": 1, "a": 1})}, `Unrecognized key: "a"`},
	}
	for what, c := range bad {
		t.Run(what, func(t *testing.T) {
			d := t.TempDir()
			path := write(t, d, "bad.json", c.script)
			_, err := InOrder([]string{d}, nil)
			var e *Error
			if !errors.As(err, &e) || !strings.HasPrefix(err.Error(), "script "+path+": ") || !strings.Contains(err.Error(), c.reason) {
				t.Errorf("%v, want %q after the path %s", err, c.reason, path)
			}
		})
	}
}

func TestAScriptsDirectoryThatDoesNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := Read([]string{missing})
	if err == nil || !strings.HasPrefix(err.Error(), "scripts directory "+missing+": cannot read: ") {
		t.Error(err)
	}
}

func TestANameThatIsStillThereAndCannotBeRead(t *testing.T) {
	d := t.TempDir()
	link := filepath.Join(d, "dangling.json")
	if err := os.Symlink(filepath.Join(d, "nowhere"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read([]string{d}); err == nil || !strings.HasPrefix(err.Error(), "script "+link+": cannot read: ") {
		t.Error(err)
	}
}

func TestAJSONEntryThatIsNotAFile(t *testing.T) {
	d := t.TempDir()
	sub := filepath.Join(d, "sub.json")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Read([]string{d}); err == nil || err.Error() != "script "+sub+": cannot read: not a regular file" {
		t.Error(err)
	}
}

func TestAFileThatIsNotUTF8(t *testing.T) {
	d := t.TempDir()
	path := write(t, d, "latin1.json", "{\"reply\": \"caf\xe9\"}")
	if _, err := Read([]string{d}); err == nil || err.Error() != "script "+path+": cannot read: not UTF-8" {
		t.Error(err)
	}
}

func TestOfSeveralBadScriptsTheOneNamedIsTheFirstInOrder(t *testing.T) {
	d := dir(t, m{"a.json": `{"reply": ` + strings.Repeat("[", 1_000_000), "b.json": "{"})
	for run := 0; run < 3; run++ {
		if _, err := Read([]string{d}); err == nil || !strings.HasPrefix(err.Error(), "script "+filepath.Join(d, "a.json")+": not JSON") {
			t.Fatalf("run %d: %.120v", run, err)
		}
	}
}

// A named pipe would wait forever for a writer; it is refused without waiting.
func TestANamedPipe(t *testing.T) {
	d := t.TempDir()
	fifo := filepath.Join(d, "pipe.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	if _, err := Read([]string{d}); err == nil || err.Error() != "script "+fifo+": cannot read: not a regular file" {
		t.Error(err)
	}
}

func TestAScriptRemovedAfterTheListingIsNotAScript(t *testing.T) {
	d := dir(t, m{"a.json": says("a", nil)})
	path := filepath.Join(d, "a.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	text, err := readRegularFile(path)
	if text != nil || err != nil {
		t.Errorf("%q, %v", text, err)
	}
}

func TestAHeaderNamedThreeTimesIsListedOnce(t *testing.T) {
	d := t.TempDir()
	write(t, d, "bad.json", m{"reply": m{"kind": "literal", "status": 429, "body": "", "headers": m{"X-A": "1", "x-a": "2", "X-a": "3"}}})
	_, err := InOrder([]string{d}, nil)
	if err == nil || !strings.HasSuffix(err.Error(), "names a field twice: x-a") {
		t.Errorf("%v", err)
	}
}
