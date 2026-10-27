package recording

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/oauth"
)

// What a real recording user would leave in an exchange, built at run time.
type planted struct {
	token, apiKey, jwt, email, other, acct, org, session, toolUUID, hash, bearer string
}

func plant() planted {
	return planted{
		token:    "tok-" + strings.Repeat("r3alS3cret", 3),
		apiKey:   "sk-ant-" + "api03-" + strings.Repeat("Zq9", 8),
		jwt:      "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiJ4eHh4eCJ9" + "." + "c2lnbmF0dXJl",
		email:    "someone" + "@" + "private-corp.example",
		other:    "git.author" + "@" + "elsewhere.example",
		acct:     realUUID(1),
		org:      realUUID(2),
		session:  realUUID(3),
		toolUUID: realUUID(4),
		hash:     strings.Repeat("ab12", 16),
		bearer:   "Bearer " + strings.Repeat("Qw8", 8),
	}
}

func (p planted) all() []string {
	return []string{p.token, p.apiKey, p.jwt, p.email, p.other, p.acct, p.org, p.session, p.toolUUID, p.hash, p.bearer}
}

func (p planted) request(userID string) string {
	msg := fmt.Sprintf("log: author %s key %s jwt %s id %s hdr %s org %s", p.other, p.apiKey, p.jwt, p.toolUUID, p.bearer, p.org)
	body := map[string]any{
		"model":    "m",
		"metadata": map[string]any{"user_id": userID},
		"system":   []any{map[string]any{"type": "text", "text": "userEmail: " + p.email}},
		"messages": []any{
			map[string]any{"role": "user", "content": "token is " + p.token},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": msg}}},
		},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func (p planted) stringUserID() string {
	return "user_" + p.hash + "_account_" + p.acct + "_session_" + p.session
}

func (p planted) objectUserID() string {
	b, _ := json.Marshal(map[string]string{"device_id": p.hash, "account_uuid": p.acct, "session_id": p.session})
	return string(b)
}

// The model echoes the email split across text_delta events, as a stream delivers it.
func (p planted) stream() []byte {
	half := len(p.email) / 2
	delta := func(s string) string {
		b, _ := json.Marshal(s)
		return `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(b) + `}}`
	}
	return sse(
		`{"type":"message_start","message":{"id":"msg_01"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		delta("you are "+p.email[:half]), delta(p.email[half:]+" and "+p.toolUUID[:20]), delta(p.toolUUID[20:]),
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	)
}

func scrubPlanted(t *testing.T, p planted, userID string) (Exchange, Counts) {
	t.Helper()
	reply, _, err := Assemble(stream, p.stream())
	if err != nil {
		t.Fatal(err)
	}
	s, counts, err := Scrub(Exchange{Request: json.RawMessage(p.request(userID)), Reply: reply, MessageID: oneID}, []string{p.token, "Bearer " + p.token}, account.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s.exchange, counts
}

func TestScrubLeavesNothingThatWasPlanted(t *testing.T) {
	a := account.Default()
	for name, userID := range map[string]string{"the string user_id": plant().stringUserID(), "the object user_id": plant().objectUserID()} {
		p := plant()
		e, counts := scrubPlanted(t, p, userID)
		saved := string(e.Request) + string(e.Reply)
		for _, secret := range p.all() {
			if strings.Contains(saved, secret) || strings.Contains(strings.ToLower(saved), strings.ToLower(secret)) {
				t.Errorf("%s: a planted value is left in the recording (%d bytes of it)", name, len(secret))
			}
		}
		if foreign := account.ForeignUUIDs([]byte(saved)); len(foreign) != 0 {
			t.Errorf("%s: foreign UUIDs at %v", name, foreign)
		}
		if !strings.Contains(saved, a.AccountUUID()) {
			t.Errorf("%s: the account's UUID did not replace the one the user_id marks", name)
		}
		for _, kind := range []Kind{Token, Email, UUID, UserHash} {
			if counts[kind] == 0 {
				t.Errorf("%s: nothing counted for %s: %v", name, kind, counts)
			}
		}
		if !strings.Contains(saved, oauth.AccessToken) || !strings.Contains(saved, a.EmailAddress()) {
			t.Errorf("%s: the mock's token and the account's email are not what replaced them", name)
		}
		if _, err := Parse(mustMarshal(t, e)); err != nil {
			t.Errorf("%s: the scrubbed exchange is no recording: %v", name, err)
		}
	}
}

func mustMarshal(t *testing.T, e Exchange) []byte {
	t.Helper()
	b, err := e.marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScrubReadsJSONEscapes(t *testing.T) {
	p := plant()
	escaped := strings.Replace(p.email, "@", `@`, 1)
	req := `{"model":"m","messages":[{"role":"user","content":"` + escaped + `"}]}`
	s, _, err := Scrub(Exchange{Request: json.RawMessage(req), Reply: json.RawMessage(oneReply), MessageID: oneID}, nil, account.Default())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(s.exchange.Request), "private-corp") {
		t.Errorf("an email written with a JSON escape survived: %s", s.exchange.Request)
	}
}

func TestScrubMapsOneUUIDToOneStandInWhateverSurroundsIt(t *testing.T) {
	p := plant()
	scrubbedContent := func(surround string) string {
		req := `{"model":"m","messages":[{"role":"user","content":"` + surround + p.toolUUID + `"}]}`
		s, _, err := Scrub(Exchange{Request: json.RawMessage(req), Reply: json.RawMessage(oneReply), MessageID: oneID}, nil, account.Default())
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.Unmarshal(s.exchange.Request, &got); err != nil {
			t.Fatal(err)
		}
		c := got.Messages[0].Content
		return c[len(c)-36:]
	}
	a, b := scrubbedContent(""), scrubbedContent(p.session+" and "+p.org+" then ")
	if a != b || !account.IsSyntheticUUID(a) {
		t.Errorf("the same UUID scrubbed to %q and %q", a, b)
	}
}

func TestScrubRefusesWhatItCannotMakeClean(t *testing.T) {
	// A secret that the mock's own token contains: replacing it puts it back.
	_, _, err := Scrub(Exchange{Request: json.RawMessage(`{"messages":[{"role":"user","content":"a-secret-` + oauth.AccessToken[5:] + `"}]}`), Reply: json.RawMessage(oneReply), MessageID: oneID},
		[]string{oauth.AccessToken[5:]}, account.Default())
	if err == nil || !strings.Contains(err.Error(), "token at .request") {
		t.Errorf("a secret the replacement contains: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), oauth.AccessToken[5:]) {
		t.Errorf("the refusal copies the value: %v", err)
	}
	p := plant()
	_, _, err = Scrub(Exchange{Request: json.RawMessage(`{"messages":[{"role":"user","content":"x"}],"` + p.email + `":1,"` + p.other + `":2}`), Reply: json.RawMessage(oneReply), MessageID: oneID}, nil, account.Default())
	if err == nil || !strings.Contains(err.Error(), "same key") {
		t.Errorf("two keys that scrub alike: %v", err)
	}
}

func TestScrubLeavesPackageVersionsAlone(t *testing.T) {
	req := `{"model":"m","messages":[{"role":"user","content":"npm i lodash@4.17.21 and mail me@example.com"}]}`
	s, counts, err := Scrub(Exchange{Request: json.RawMessage(req), Reply: json.RawMessage(oneReply), MessageID: oneID}, nil, account.Default())
	if err != nil {
		t.Fatal(err)
	}
	out := string(mustMarshal(t, s.exchange))
	if !strings.Contains(out, "lodash@4.17.21") || strings.Contains(out, "me@example.com") || counts[Email] != 1 {
		t.Errorf("%s %v", out, counts)
	}
}
