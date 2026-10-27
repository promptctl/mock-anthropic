package recording

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/promptctl/mock-anthropic/internal/account"
)

func TestScrubbingARequestTwiceChangesNothingAndKeysAlike(t *testing.T) {
	email, id := "someone"+"@"+"private-corp.example", fmt.Sprintf("%08x-1111-4222-8333-%012x", 0xabcdef00+9, 9)
	raw := `{"diagnostics":{"previous_message_id":null},"messages":[{"role":"user","content":"` + email + ` ` + id + `"}]}`
	once, err := scrubRequest([]byte(raw), account.Default())
	if err != nil || strings.Contains(string(once), email) || strings.Contains(string(once), id) {
		t.Fatalf("scrub: %s, %v", once, err)
	}
	twice, err := scrubRequest(once, account.Default())
	if err != nil || string(once) != string(twice) {
		t.Errorf("scrubbing is not idempotent: %s then %s (%v)", once, twice, err)
	}
	a, _ := Canonicalize([]byte(raw), account.Default())
	b, _ := Canonicalize(once, account.Default())
	if a.Facts()["key"] == nil || fmt.Sprint(a.Facts()) != fmt.Sprint(b.Facts()) {
		t.Errorf("live and stored keys differ: %v vs %v", a.Facts(), b.Facts())
	}
}

func TestAssembleKeepsTheMessagesRealID(t *testing.T) {
	_, id, err := Assemble(stream, sse(goodStream()[0]...))
	if err != nil || id != "msg_1" {
		t.Errorf("stream id %q, %v", id, err)
	}
	_, id, err = Assemble("application/json", []byte(`{"id":"msg_json","content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"}`))
	if err != nil || id != "msg_json" {
		t.Errorf("json id %q, %v", id, err)
	}
	if _, _, err = Assemble("application/json", []byte(`{"content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"}`)); err == nil {
		t.Error("a message with no id was assembled: a recording of it could never be continued")
	}
}

func TestAKeyFactCutsALongPromptOnACharacterBoundary(t *testing.T) {
	// Each character is three bytes, so byte maxText falls inside one.
	prompt := strings.Repeat("\u65e5", maxText)
	c, err := Canonicalize([]byte(`{"diagnostics":{"previous_message_id":null},"messages":[{"role":"user","content":"`+prompt+`"}]}`), account.Default())
	if err != nil {
		t.Fatal(err)
	}
	last := c.Facts()["key"].(map[string]any)["last_text"].(string)
	if !utf8.ValidString(last) || len(last) > maxText || last == "" {
		t.Errorf("last_text %q", last)
	}
}
