package recording

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
)

// Refused is an exchange the recorder will not save, and why: a response it cannot fully read is never
// saved as it came. [LAW:no-silent-failure] The reason reaches the request's record and the totals.
type Refused struct{ Why string }

func (r *Refused) Error() string { return r.Why }

// ErrIncomplete is an event stream that ended without its `message_stop`: upstream cut it short.
var ErrIncomplete = errors.New("the event stream ended without message_stop")

func refuse(format string, args ...any) error { return &Refused{fmt.Sprintf(format, args...)} }

// Assemble reads a successful Messages response, a JSON message or a server-sent event stream by its
// Content-Type, into the reply form (script.ParseReply's "message"). Only what a replay renders from is
// kept: the content blocks and the stop reason. Usage, model, ids and signatures are not.
func Assemble(contentType string, body []byte) (json.RawMessage, error) {
	media, _, _ := mime.ParseMediaType(contentType)
	var a assembly
	var err error
	switch media {
	case "text/event-stream":
		err = a.stream(body)
	case "application/json":
		err = a.message(body)
	default:
		return nil, refuse("response content type %q is neither a JSON message nor an event stream", contentType)
	}
	if err != nil {
		return nil, err
	}
	return a.reply()
}

type block struct {
	Type     string          `json:"type"`
	Text     *string         `json:"text,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Thinking *string         `json:"thinking,omitempty"`
}

type assembly struct {
	blocks []*block
	json   map[int]*strings.Builder // a tool_use block's input, as its partial_json arrived
	stop   string
}

func (a *assembly) reply() (json.RawMessage, error) {
	if len(a.blocks) == 0 {
		return nil, refuse("the response holds no content blocks")
	}
	out, err := json.Marshal(struct {
		Kind       string   `json:"kind"`
		Content    []*block `json:"content"`
		StopReason string   `json:"stop_reason"`
	}{"message", a.blocks, a.stop})
	if err != nil {
		return nil, err
	}
	// [LAW:single-enforcer] script.ParseReply decides what a message reply may be; a stop reason or a
	// block it does not accept is refused here, with its words.
	if err := (Exchange{Request: json.RawMessage(`{}`), Reply: out}).check(); err != nil {
		return nil, refuse("%v", err)
	}
	return out, nil
}

func (a *assembly) add(raw json.RawMessage) error {
	var b struct {
		Type     string          `json:"type"`
		Text     *string         `json:"text"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
		Thinking *string         `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return refuse("a content block is not an object: %v", err)
	}
	switch b.Type {
	case "text":
		if b.Text == nil {
			return refuse("a text block has no text")
		}
	case "thinking":
		if b.Thinking == nil {
			return refuse("a thinking block has no thinking")
		}
	case "tool_use":
		if len(b.Input) == 0 {
			b.Input = json.RawMessage(`{}`)
		}
	default:
		return refuse("content block type %q", b.Type)
	}
	a.blocks = append(a.blocks, &block{Type: b.Type, Text: b.Text, Name: b.Name, Input: b.Input, Thinking: b.Thinking})
	return nil
}

// message reads a non-streamed response: the message itself.
func (a *assembly) message(body []byte) error {
	var m struct {
		Content    []json.RawMessage `json:"content"`
		StopReason string            `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return refuse("the response is not a JSON message: %v", err)
	}
	for _, raw := range m.Content {
		if err := a.add(raw); err != nil {
			return err
		}
	}
	a.stop = m.StopReason
	return nil
}

// stream reads the Messages event stream. Its events are the API's full set: `ping` is dropped, an
// `error` event or any event or delta this does not know refuses the recording by name.
func (a *assembly) stream(body []byte) error {
	a.json = map[int]*strings.Builder{}
	stopped := false
	err := events(body, func(data []byte) error {
		var ev struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			ContentBlock json.RawMessage `json:"content_block"`
			Delta        struct {
				Type        string  `json:"type"`
				Text        *string `json:"text"`
				Thinking    *string `json:"thinking"`
				PartialJSON *string `json:"partial_json"`
				StopReason  *string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			return refuse("an event is not JSON: %v", err)
		}
		switch ev.Type {
		case "ping", "message_start", "content_block_stop":
		case "message_stop":
			stopped = true
		case "error":
			return refuse("upstream error event")
		case "content_block_start":
			if ev.Index != len(a.blocks) {
				return refuse("content block %d started out of order", ev.Index)
			}
			return a.add(ev.ContentBlock)
		case "content_block_delta":
			return a.delta(ev.Index, ev.Delta.Type, ev.Delta.Text, ev.Delta.Thinking, ev.Delta.PartialJSON)
		case "message_delta":
			if ev.Delta.StopReason != nil {
				a.stop = *ev.Delta.StopReason
			}
		default:
			return refuse("event type %q", ev.Type)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !stopped {
		return ErrIncomplete
	}
	for i, in := range a.json {
		text := strings.TrimSpace(in.String())
		if text == "" {
			continue
		}
		if !json.Valid([]byte(text)) {
			return refuse("tool input of content block %d is not JSON", i)
		}
		a.blocks[i].Input = json.RawMessage(text)
	}
	return nil
}

func (a *assembly) delta(index int, kind string, text, thinking, partial *string) error {
	if index < 0 || index >= len(a.blocks) {
		return refuse("a delta for content block %d, which has not started", index)
	}
	b := a.blocks[index]
	switch kind {
	case "text_delta":
		if b.Type != "text" || text == nil {
			return refuse("a text_delta for a %s block", b.Type)
		}
		*b.Text += *text
	case "thinking_delta":
		if b.Type != "thinking" || thinking == nil {
			return refuse("a thinking_delta for a %s block", b.Type)
		}
		*b.Thinking += *thinking
	case "input_json_delta":
		if b.Type != "tool_use" || partial == nil {
			return refuse("an input_json_delta for a %s block", b.Type)
		}
		if a.json[index] == nil {
			a.json[index] = &strings.Builder{}
		}
		a.json[index].WriteString(*partial)
	case "signature_delta":
	default:
		return refuse("delta type %q", kind)
	}
	return nil
}

// events calls fn with the data of each server-sent event in body (WHATWG HTML 9.2): the `data` lines of
// one event joined by newlines; every other field and every comment is ignored.
func events(body []byte, fn func(data []byte) error) error {
	r := bufio.NewReader(bytes.NewReader(body))
	var data [][]byte
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		joined := bytes.Join(data, []byte("\n"))
		data = nil
		return fn(joined)
	}
	for {
		line, err := r.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0 && err == nil:
			if ferr := flush(); ferr != nil {
				return ferr
			}
		case bytes.HasPrefix(line, []byte("data:")):
			data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" ")))
		}
		if err == io.EOF {
			return flush()
		}
		if err != nil {
			return err
		}
	}
}
