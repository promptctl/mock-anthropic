// Package messages is the one reading of a request body shaped like the Messages API's (PROJECT.md
// "Messages"). [LAW:one-source-of-truth] Every part of the mock that answers such a request reads the
// body through this package, never on its own, so the two endpoints refuse the same messages for the
// same reason and count one conversation the same.
package messages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf16"
)

// CountTokensRequest is a `POST /v1/messages/count_tokens` body that parsed. [LAW:parse-dont-validate]
// Only ParseCountTokens makes one, so a value of this type is a request the base accepts.
type CountTokensRequest struct{ InputTokens int }

// ParseCountTokens reads a token count request: the base alone. No script is matched against it, so it
// needs no last user message. The error is the reason the body is not one, for the generator to decline
// with.
func ParseCountTokens(raw []byte) (CountTokensRequest, error) {
	var body struct {
		Model    json.RawMessage `json:"model"`
		System   json.RawMessage `json:"system"`
		Messages json.RawMessage `json:"messages"`
		Tools    json.RawMessage `json:"tools"`
	}
	const notOne = "body is not a token count request: %w"
	if err := decodeObject(raw, &body); err != nil {
		return CountTokensRequest{}, fmt.Errorf(notOne, err)
	}
	if err := checkBase(body.Model, body.System, body.Messages); err != nil {
		return CountTokensRequest{}, fmt.Errorf(notOne, err)
	}
	return CountTokensRequest{InputTokens: inputTokens(body.System, body.Messages, body.Tools)}, nil
}

// Request is a `POST /v1/messages` body that parsed. [LAW:parse-dont-validate] Only ParseRequest makes
// one. Raw is the body as sent, for matching anywhere in it. LastUserTexts are the texts of the last
// `user` message, the current turn: a `text` block's text and a `tool_result` block's text content, and
// nothing from any other block, so ids, `type` values and image data are never matchable. A
// `system`-role message contributes no text to any field, so it is reached only through Raw.
// SystemTexts are the system prompt's blocks, each one text.
type Request struct {
	Raw           string
	Model         string
	MaxTokens     int
	Stream        bool
	SystemTexts   []string
	LastUserTexts []string
	InputTokens   int
}

// ParseRequest reads a Messages request: the base, then `max_tokens` (a positive integer) and `stream`
// (false when absent), and a last user message for a script to be matched against. The error is the
// reason the body is not one, for the generator to decline with.
func ParseRequest(raw []byte) (Request, error) {
	var body struct {
		Model     json.RawMessage `json:"model"`
		System    json.RawMessage `json:"system"`
		Messages  json.RawMessage `json:"messages"`
		Tools     json.RawMessage `json:"tools"`
		MaxTokens json.RawMessage `json:"max_tokens"`
		Stream    json.RawMessage `json:"stream"`
	}
	const notOne = "body is not a Messages request: %w"
	if err := decodeObject(raw, &body); err != nil {
		return Request{}, fmt.Errorf(notOne, err)
	}
	if err := checkBase(body.Model, body.System, body.Messages); err != nil {
		return Request{}, fmt.Errorf(notOne, err)
	}
	r := Request{Raw: string(raw), InputTokens: inputTokens(body.System, body.Messages, body.Tools)}
	// A number with a fraction, or a string, is not an integer: decoding into int refuses both.
	if err := json.Unmarshal(body.MaxTokens, &r.MaxTokens); err != nil || r.MaxTokens < 1 {
		return Request{}, fmt.Errorf(notOne, errors.New("max_tokens: expected a positive integer"))
	}
	if body.Stream != nil {
		if err := json.Unmarshal(body.Stream, &r.Stream); err != nil || bytes.Equal(body.Stream, []byte("null")) {
			return Request{}, fmt.Errorf(notOne, errors.New("stream: expected a boolean"))
		}
	}
	// checkBase passed, so each decode below succeeds; what it holds is only read here.
	json.Unmarshal(body.Model, &r.Model)
	r.SystemTexts = systemTexts(body.System)
	var list []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	json.Unmarshal(body.Messages, &list)
	found := false
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Role == "user" {
			r.LastUserTexts, found = contentTexts(list[i].Content), true
			break
		}
	}
	if !found {
		return Request{}, fmt.Errorf(notOne, errors.New("messages: has no user message"))
	}
	return r, nil
}

// absent is an optional field left out or sent as null, which the API reads alike.
func absent(raw json.RawMessage) bool { return raw == nil || bytes.Equal(raw, []byte("null")) }

// systemTexts is a checked system prompt as texts: a string is one, and a list of text blocks is one each.
func systemTexts(raw json.RawMessage) []string {
	if absent(raw) {
		return []string{}
	}
	return contentTexts(raw)
}

// contentTexts is checked content as the texts a script can match in it: a string is one text, and a
// list of blocks is each `text` block's text and each `tool_result` block's content, in order.
// [LAW:one-type-per-behavior] A message's content and a tool result's content are one shape.
func contentTexts(raw json.RawMessage) []string {
	texts := []string{}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return append(texts, text)
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	json.Unmarshal(raw, &blocks)
	for _, b := range blocks {
		switch b.Type {
		case textType:
			texts = append(texts, b.Text)
		case toolResultType:
			if b.Content != nil {
				texts = append(texts, contentTexts(b.Content)...)
			}
		}
	}
	return texts
}

// Plausible, not counted: about four characters to a token, as English text runs.
func tokens(text string) int {
	units := 0
	for _, r := range text {
		units += utf16.RuneLen(r)
	}
	return (units + 3) / 4
}

// [LAW:one-source-of-truth] A request's input tokens, the one count both a token count answer and a
// Messages reply's usage give: its system prompt, messages and tools as sent, and none of the fields
// that shape only the reply (`max_tokens`, `stream`, `metadata`). A field not sent counts as null.
func inputTokens(system, messages, tools json.RawMessage) int {
	var text bytes.Buffer
	text.WriteByte('[')
	for i, part := range []json.RawMessage{system, messages, tools} {
		if i > 0 {
			text.WriteByte(',')
		}
		if part == nil {
			text.WriteString("null")
			continue
		}
		// The parts came out of a decoder that accepted them, so they compact.
		if err := json.Compact(&text, part); err != nil {
			panic(fmt.Sprintf("a part the decoder accepted does not compact: %v", err))
		}
	}
	text.WriteByte(']')
	return tokens(text.String())
}

// decodeObject reads raw as exactly one JSON object into into. Unmarshal refuses trailing data, so
// "is it JSON" and "is it an object" are each answered once, with their own reason.
func decodeObject(raw []byte, into any) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("body is not JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("expected an object, got %s", kindOf(value))
	}
	return json.Unmarshal(raw, into)
}

// checkBase is what a Messages request and a token count request share: `model` is a non-empty
// string, `system` is absent, a string or a list of text blocks, and `messages` is a non-empty list of
// messages. A block of a type read here that is malformed fails rather than passing as some other.
func checkBase(model, system, messages json.RawMessage) error {
	var name string
	if err := json.Unmarshal(model, &name); err != nil || name == "" {
		return errors.New("model: expected a non-empty string")
	}
	if !absent(system) {
		if err := checkSystem(system); err != nil {
			return fmt.Errorf("system: %w", err)
		}
	}
	var list []json.RawMessage
	if err := json.Unmarshal(messages, &list); err != nil || len(list) == 0 {
		return errors.New("messages: expected a non-empty list")
	}
	for i, message := range list {
		if err := checkMessage(message); err != nil {
			return fmt.Errorf("messages[%d]: %w", i, err)
		}
	}
	return nil
}

// The system prompt is a string or a list of text blocks, the only block type the API admits there.
func checkSystem(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return errors.New("expected a string or a list of text blocks")
	}
	for i, block := range blocks {
		if err := checkText(block); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}
	return nil
}

// `system` is a role Claude Code sends in `messages` (2.1.289 puts a hook's added context there),
// though the API's own documentation names only `user` and `assistant`.
func checkMessage(raw json.RawMessage) error {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return errors.New("expected an object")
	}
	switch message.Role {
	case "user", "assistant", "system":
	default:
		return fmt.Errorf("role: expected user, assistant or system, got %q", message.Role)
	}
	// Unmarshal reads null into a string as a no-op, so a null content would pass as an empty one.
	if bytes.Equal(bytes.TrimSpace(message.Content), []byte("null")) {
		return errors.New("content: expected a string or a list of blocks, got null")
	}
	if err := checkContent(message.Content, checkMessageBlock); err != nil {
		return fmt.Errorf("content: %w", err)
	}
	return nil
}

// [LAW:one-type-per-behavior] A message's content and a tool result's content are one shape, a string
// or a list of blocks, and differ only in the blocks they admit.
func checkContent(raw json.RawMessage, block func(json.RawMessage) error) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return errors.New("expected a string or a list of blocks")
	}
	for i, b := range blocks {
		if err := block(b); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}
	return nil
}

const (
	textType       = "text"
	toolResultType = "tool_result"
)

func blockType(raw json.RawMessage) (string, error) {
	var block struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &block); err != nil || block.Type == "" {
		return "", errors.New("expected an object with a type")
	}
	return block.Type, nil
}

func checkText(raw json.RawMessage) error {
	var block struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(raw, &block); err != nil || block.Type != textType {
		return errors.New("expected a text block")
	}
	if block.Text == nil {
		return errors.New("a text block must have its text")
	}
	return nil
}

func checkMessageBlock(raw json.RawMessage) error {
	kind, err := blockType(raw)
	if err != nil {
		return err
	}
	switch kind {
	case textType:
		return checkText(raw)
	case toolResultType:
		var block struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return errors.New("expected a tool_result block")
		}
		if block.Content == nil {
			return nil
		}
		return checkContent(block.Content, checkToolResultBlock)
	}
	return nil
}

func checkToolResultBlock(raw json.RawMessage) error {
	kind, err := blockType(raw)
	if err != nil {
		return err
	}
	switch kind {
	case textType:
		return checkText(raw)
	case toolResultType:
		return errors.New("a tool_result block cannot hold a tool_result block")
	}
	return nil
}

func kindOf(value any) string {
	switch value.(type) {
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	}
	return "a number"
}
