package recording

import (
	"errors"
	"strings"
	"testing"
)

func sse(events ...string) []byte {
	var b strings.Builder
	for _, e := range events {
		typ := e[strings.Index(e, `"type":"`)+8:]
		typ = typ[:strings.Index(typ, `"`)]
		b.WriteString("event: " + typ + "\ndata: " + e + "\n\n")
	}
	return []byte(b.String())
}

const stream = "text/event-stream; charset=utf-8"

func goodStream() [][]string {
	return [][]string{{
		`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":3}}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"he"}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"llo"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	}}
}

func TestAssembleReadsTheWholeEventSet(t *testing.T) {
	reply, err := Assemble(stream, sse(goodStream()[0]...))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"message","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"hello"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}],"stop_reason":"tool_use"}`
	if string(reply) != want {
		t.Errorf("reply\n got %s\nwant %s", reply, want)
	}
}

func TestAssembleReadsAJSONMessage(t *testing.T) {
	body := `{"id":"msg_1","model":"m","usage":{},"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`
	reply, err := Assemble("application/json", []byte(body))
	if err != nil || string(reply) != `{"kind":"message","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}` {
		t.Errorf("%s, %v", reply, err)
	}
}

func TestAssembleRefusesWhatItCannotFullyRead(t *testing.T) {
	good := goodStream()[0]
	with := func(old, new string) []byte {
		out := make([]string, len(good))
		for i, e := range good {
			out[i] = strings.Replace(e, old, new, 1)
		}
		return sse(out...)
	}
	for name, c := range map[string]struct {
		contentType string
		body        []byte
		want        string
	}{
		"an unknown event":                   {stream, with(`"type":"message_start"`, `"type":"mystery"`), `event type "mystery"`},
		"an error event":                     {stream, with(`"type":"message_start"`, `"type":"error"`), "upstream error event"},
		"an unknown block":                   {stream, with(`"type":"thinking","thinking":""`, `"type":"redacted_thinking","data":"x"`), `content block type "redacted_thinking"`},
		"an unknown delta":                   {stream, with(`"type":"text_delta"`, `"type":"citations_delta"`), `delta type "citations_delta"`},
		"a stop reason replay cannot render": {stream, with(`"stop_reason":"tool_use"`, `"stop_reason":"pause_turn"`), "stop_reason"},
		"a content type":                     {"text/plain", []byte("x"), "content type"},
		"a message that is not":              {"application/json", []byte("[]"), "not a JSON message"},
		"no content":                         {"application/json", []byte(`{"content":[],"stop_reason":"end_turn"}`), "no content blocks"},
	} {
		_, err := Assemble(c.contentType, c.body)
		var refused *Refused
		if !errors.As(err, &refused) || !strings.Contains(refused.Why, c.want) {
			t.Errorf("%s: %v, want a refusal mentioning %q", name, err, c.want)
		}
	}
}

func TestAssembleNamesAStreamCutShort(t *testing.T) {
	good := goodStream()[0]
	if _, err := Assemble(stream, sse(good[:len(good)-1]...)); !errors.Is(err, ErrIncomplete) {
		t.Errorf("a stream without message_stop: %v", err)
	}
}
