package messagesgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/messages"
	"github.com/promptctl/mock-anthropic/internal/script"
)

// render is a script's reply as the response the request asked for (PROJECT.md "Messages"). A message
// reply is rendered as the Messages API sends an assistant turn, streamed or as one JSON message. A
// literal reply is sent as written: its framing is the server's to enforce, not this package's.
// [LAW:nothing-unseen] The facts describe what was sent and are built from the same values as the
// response: which form the reply took and, for a message, its id, the key that joins the record to the
// consumer's transcript.
func render(ctx context.Context, reply script.Reply, request messages.Request, seen *occurrences) (generator.Response, generator.Facts, error) {
	switch reply := reply.(type) {
	case script.LiteralReply:
		response := generator.Response{Status: reply.Status, Header: reply.Header.Clone(), Body: bytes.NewReader(reply.Body)}
		return response, generator.Facts{"kind": "literal", "status": reply.Status}, nil
	case script.MessageReply:
		m := assistantMessage(reply, request, seen.next(request.Raw))
		if !request.Stream {
			body, err := marshal(m.message)
			if err != nil {
				return generator.Response{}, nil, err
			}
			response := generator.Response{Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: bytes.NewReader(body)}
			return response, generator.Facts{"kind": "json", "id": m.message.ID, "stop_reason": *m.message.StopReason}, nil
		}
		frames, err := m.frames()
		if err != nil {
			return generator.Response{}, nil, err
		}
		response := generator.Response{
			Status: http.StatusOK,
			Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}, "Cache-Control": {"no-cache"}},
			Body:   &stream{ctx: ctx, frames: frames, delay: reply.EventDelay},
		}
		facts := generator.Facts{"kind": "stream", "id": m.message.ID, "stop_reason": *m.message.StopReason, "event_delay_ms": reply.EventDelay.Milliseconds()}
		return response, facts, nil
	}
	return generator.Response{}, nil, fmt.Errorf("a script gave a reply of type %T, which is neither a message nor a literal", reply)
}

// The API's content blocks, as a reply's blocks become once they have ids and signatures. Their fields
// are not omitempty: an empty `text` is still sent.
type (
	textBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	toolUseBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	thinkingBlock struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}
)

type usage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type message struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Role         string  `json:"role"`
	Model        string  `json:"model"`
	Content      []any   `json:"content"`
	StopReason   *string `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
	Usage        usage   `json:"usage"`
}

// part is one content block in the three forms it takes: whole, as content_block_start opens it (its
// streamed field empty), and as the deltas that fill that field in. Text is what the deltas carry.
type part struct {
	whole, start any
	text         string
	delta        func(chunk string) map[string]any
	// after is a delta that follows the text: a thinking block's signature arrives in one
	// signature_delta, as the API sends it.
	after []map[string]any
}

type assistant struct {
	message message
	parts   []part
}

// [LAW:types-are-the-program] Every id is a function of the raw body and how many times that body has
// been answered: the same request sequence gets the same ids in every run, and no two answers share
// one. The body and its occurrence are hashed once; each block's id hashes that digest with the block's
// type and index, joined by NUL, which none of them holds, so no two part lists hash alike.
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

const idHex = 24

func assistantMessage(reply script.MessageReply, request messages.Request, occurrence int) assistant {
	requestDigest := digest(request.Raw, fmt.Sprint(occurrence))
	a := assistant{}
	output := 0
	for index, b := range reply.Content {
		p := blockPart(b, requestDigest, index)
		a.parts = append(a.parts, p)
		a.message.Content = append(a.message.Content, p.whole)
		output += messages.Tokens(p.text)
	}
	stop := string(reply.StopReason)
	a.message = message{
		ID:         "msg_" + requestDigest[:idHex],
		Type:       "message",
		Role:       "assistant",
		Model:      request.Model,
		Content:    a.message.Content,
		StopReason: &stop,
		Usage:      usage{InputTokens: request.InputTokens, OutputTokens: min(request.MaxTokens, max(1, output))},
	}
	return a
}

func blockPart(b script.Block, requestDigest string, index int) part {
	switch b := b.(type) {
	case script.Text:
		return part{
			whole: textBlock{"text", b.Text}, start: textBlock{"text", ""}, text: b.Text,
			delta: func(c string) map[string]any { return map[string]any{"type": "text_delta", "text": c} },
		}
	case script.ToolUse:
		// The script parser accepted the input as a JSON object, so it compacts. The whole message
		// is marshaled without HTML escaping, so these bytes are what it carries and what the deltas carry.
		var compact bytes.Buffer
		if err := json.Compact(&compact, b.Input); err != nil {
			panic(fmt.Sprintf("a tool input the script parser accepted does not compact: %v", err))
		}
		input := compact.Bytes()
		id := "toolu_" + digest(requestDigest, "tool_use", fmt.Sprint(index))[:idHex]
		return part{
			whole: toolUseBlock{"tool_use", id, b.Name, json.RawMessage(input)},
			start: toolUseBlock{"tool_use", id, b.Name, json.RawMessage("{}")},
			text:  string(input),
			delta: func(c string) map[string]any { return map[string]any{"type": "input_json_delta", "partial_json": c} },
		}
	case script.Thinking:
		raw, _ := hex.DecodeString(digest(requestDigest, "thinking", fmt.Sprint(index)))
		signature := base64.StdEncoding.EncodeToString(raw)
		return part{
			whole: thinkingBlock{"thinking", b.Thinking, signature}, start: thinkingBlock{"thinking", "", ""}, text: b.Thinking,
			delta: func(c string) map[string]any { return map[string]any{"type": "thinking_delta", "thinking": c} },
			after: []map[string]any{{"type": "signature_delta", "signature": signature}},
		}
	}
	panic(fmt.Sprintf("a script block of type %T is not text, tool_use or thinking", b))
}

// Code points per delta: enough deltas that a short reply visibly streams. Split by code point, so no
// delta ends in half a surrogate pair.
const chunkPoints = 16

func chunks(text string) []string {
	points := []rune(text)
	out := []string{}
	for len(points) > 0 {
		n := min(chunkPoints, len(points))
		out = append(out, string(points[:n]))
		points = points[n:]
	}
	return out
}

// marshal is JSON as the API writes it: `<`, `>` and `&` literal, not escaped, so a tool input reaches the
// client as the bytes the script wrote.
func marshal(v any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// frames is [LAW:one-source-of-truth] the stream derived from the message the JSON reply would be, so a
// client that accumulates the deltas builds that same message. The events and their order are the API's.
func (a assistant) frames() ([][]byte, error) {
	m := a.message
	events := []map[string]any{{
		"type": "message_start",
		"message": message{
			ID: m.ID, Type: m.Type, Role: m.Role, Model: m.Model, Content: []any{},
			Usage: usage{InputTokens: m.Usage.InputTokens, OutputTokens: 1},
		},
	}}
	for index, p := range a.parts {
		events = append(events, map[string]any{"type": "content_block_start", "index": index, "content_block": p.start})
		for _, c := range chunks(p.text) {
			events = append(events, map[string]any{"type": "content_block_delta", "index": index, "delta": p.delta(c)})
		}
		for _, d := range p.after {
			events = append(events, map[string]any{"type": "content_block_delta", "index": index, "delta": d})
		}
		events = append(events, map[string]any{"type": "content_block_stop", "index": index})
	}
	events = append(events,
		map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": *m.StopReason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": m.Usage.OutputTokens}},
		map[string]any{"type": "message_stop"},
	)
	frames := make([][]byte, len(events))
	for i, e := range events {
		data, err := marshal(e)
		if err != nil {
			return nil, err
		}
		frames[i] = fmt.Appendf(nil, "event: %s\ndata: %s\n\n", e["type"], data)
	}
	return frames, nil
}

// stream is the body of a streamed reply: one frame per event, each after the first preceded by one
// wait. [LAW:no-ambient-temporal-coupling] It owns its timer and stops when the request's context ends,
// so a client that leaves aborts the wait in progress.
type stream struct {
	ctx     context.Context
	frames  [][]byte
	delay   time.Duration
	pending []byte
	started bool
}

func (s *stream) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if len(s.pending) == 0 {
		if len(s.frames) == 0 {
			return 0, io.EOF
		}
		// A delay of 0 is no wait at all, not a zero-length timer.
		if s.started && s.delay > 0 {
			timer := time.NewTimer(s.delay)
			select {
			case <-timer.C:
			case <-s.ctx.Done():
				timer.Stop()
				return 0, s.ctx.Err()
			}
		}
		s.started = true
		s.pending, s.frames = s.frames[0], s.frames[1:]
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
