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

// Plausible, not counted: about four characters to a token, as English text runs.
func tokens(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r >= 0x10000 {
			units++ // a surrogate pair
		}
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

// decodeObject reads raw as exactly one JSON object into into: Unmarshal refuses trailing data and
// any value that is not an object, so no second pass can disagree with the first.
func decodeObject(raw []byte, into any) error {
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("not a JSON object: %w", err)
	}
	return nil
}

// checkBase is what a Messages request and a token count request share: `model` is a non-empty
// string, `system` is absent, a string or a list of text blocks, and `messages` is a non-empty list of
// messages. A block of a type read here that is malformed fails rather than passing as some other.
func checkBase(model, system, messages json.RawMessage) error {
	var name string
	if err := json.Unmarshal(model, &name); err != nil || name == "" {
		return errors.New("model: expected a non-empty string")
	}
	if system != nil {
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
