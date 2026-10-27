package builtin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/builtin"
	"github.com/promptctl/mock-anthropic/internal/messages"
	"github.com/promptctl/mock-anthropic/internal/script"
)

type block = map[string]any

// request is a Messages request with the given system blocks, last user text and max_tokens.
func request(t *testing.T, system []string, user string, maxTokens int) messages.Request {
	t.Helper()
	blocks := []block{}
	for _, text := range system {
		blocks = append(blocks, block{"type": "text", "text": text})
	}
	raw, _ := json.Marshal(block{
		"model": "claude-sonnet-5-5", "max_tokens": maxTokens, "system": blocks,
		"messages": []block{{"role": "user", "content": user}},
	})
	r, err := messages.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func answer(t *testing.T, r messages.Request) script.Selection {
	t.Helper()
	scripts, err := script.InOrder(nil, builtin.All())
	if err != nil {
		t.Fatal(err)
	}
	return script.FirstReply(scripts, r)
}

func matched(t *testing.T, r messages.Request) (string, script.MessageReply) {
	t.Helper()
	sel := answer(t, r)
	m, ok := sel.(script.Matched)
	if !ok {
		t.Fatalf("no built-in answered: %+v", sel)
	}
	reply, ok := m.Reply.(script.MessageReply)
	if !ok {
		t.Fatalf("reply is %T", m.Reply)
	}
	return m.Origin, reply
}

func text(t *testing.T, reply script.MessageReply) string {
	t.Helper()
	if len(reply.Content) != 1 {
		t.Fatalf("content %+v", reply.Content)
	}
	return reply.Content[0].(script.Text).Text
}

const (
	billing    = "x-anthropic-billing-header: cc_version=2.1.289"
	classifier = "You are a security monitor for autonomous AI coding agents. "
)

func TestSessionTitleBothPhrasings(t *testing.T) {
	for _, opening := range []string{
		"You are naming a coding session so the user can pick it out of a long list of sessions. Reply with JSON.",
		"Generate a concise, sentence-case title for this session.",
	} {
		origin, reply := matched(t, request(t, []string{billing, opening}, "fix the bug", 100))
		if origin != "built-in: session title" || reply.StopReason != script.StopEndTurn {
			t.Errorf("%q: origin %q, stop %q", opening, origin, reply.StopReason)
		}
		var title struct{ Title string }
		if err := json.Unmarshal([]byte(text(t, reply)), &title); err != nil || title.Title != "Mock session" {
			t.Errorf("%q: text %q", opening, text(t, reply))
		}
	}
}

// A main turn's system block opens with Claude Code's own prompt, so quoting a title prompt does not match.
func TestSessionTitlePhraseQuotedMidBlockDoesNotMatch(t *testing.T) {
	r := request(t, []string{"You are Claude Code. Generate a concise, sentence-case title"}, "hi", 100)
	if _, ok := answer(t, r).(script.Unmatched); !ok {
		t.Fatal("a title phrase inside a main-turn block was answered")
	}
}

func TestClassifierAnswersInTheFormatAsked(t *testing.T) {
	for _, c := range []struct{ name, asks, origin, want string }{
		{"severity", "Answer <severity>N</severity> from 0 to 100.", "built-in: auto mode classifier, severity", "<severity>0</severity>"},
		{"block", "Answer <block>no</block> to allow.", "built-in: auto mode classifier, block", "<block>no</block>"},
	} {
		origin, reply := matched(t, request(t, []string{billing, classifier + c.asks}, "rm -rf build", 100))
		if origin != c.origin || text(t, reply) != c.want || reply.StopReason != script.StopEndTurn {
			t.Errorf("%s: origin %q, text %q, stop %q", c.name, origin, text(t, reply), reply.StopReason)
		}
	}
}

func TestClassifierToolCall(t *testing.T) {
	asks := "Use the classify_result tool to report your classification."
	origin, reply := matched(t, request(t, []string{classifier + asks}, "ls", 100))
	if origin != "built-in: auto mode classifier, classify_result tool" || reply.StopReason != script.StopToolUse {
		t.Fatalf("origin %q, stop %q", origin, reply.StopReason)
	}
	call := reply.Content[0].(script.ToolUse)
	var input map[string]any
	if err := json.Unmarshal(call.Input, &input); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"thinking": "", "shouldBlock": false, "reason": "Allowed by default."}
	if call.Name != "classify_result" || !reflect.DeepEqual(input, want) {
		t.Fatalf("%s %v", call.Name, input)
	}
}

// A classifier prompt asking for none of the formats is a 400, not a guessed answer.
func TestClassifierAskingNoKnownFormatIsUnmatched(t *testing.T) {
	if _, ok := answer(t, request(t, []string{classifier + "Reply in haiku."}, "ls", 100)).(script.Unmatched); !ok {
		t.Fatal("answered a classifier prompt that asks for no known format")
	}
}

func TestQuotaProbe(t *testing.T) {
	origin, reply := matched(t, request(t, nil, "quota", 1))
	if origin != "built-in: quota probe" || text(t, reply) != "OK" || reply.StopReason != script.StopMaxTokens {
		t.Fatalf("origin %q, text %q, stop %q", origin, text(t, reply), reply.StopReason)
	}
}

func TestQuotaProbeNeedsOneTokenAndExactlyQuota(t *testing.T) {
	for name, r := range map[string]messages.Request{
		"more tokens":   request(t, nil, "quota", 2),
		"other text":    request(t, nil, "quota please", 1),
		"quota, padded": request(t, nil, " quota", 1),
	} {
		if _, ok := answer(t, r).(script.Unmatched); !ok {
			t.Errorf("%s: answered", name)
		}
	}
}

// A consumer script that matches one of these requests wins over the built-in answer.
func TestConsumerScriptTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := `{"match":{"lastUserContains":["quota"]},"reply":{"kind":"message","content":[{"type":"text","text":"mine"}],"stop_reason":"end_turn"}}`
	if err := os.WriteFile(filepath.Join(dir, "quota.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts, err := script.InOrder([]string{dir}, builtin.All())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := script.FirstReply(scripts, request(t, nil, "quota", 1)).(script.Matched)
	if !ok || m.Origin != filepath.Join(dir, "quota.json") || text(t, m.Reply.(script.MessageReply)) != "mine" {
		t.Fatalf("%+v", m)
	}
}

// The built-ins in the order they are tried; a built-in added or dropped changes this on purpose.
func TestTheBuiltInsAndTheirOrder(t *testing.T) {
	got := []string{}
	for _, b := range builtin.All() {
		got = append(got, b.Origin)
	}
	want := []string{
		"built-in: session title",
		"built-in: auto mode classifier, severity",
		"built-in: auto mode classifier, block",
		"built-in: auto mode classifier, classify_result tool",
		"built-in: quota probe",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("built-ins %q", got)
	}
}
