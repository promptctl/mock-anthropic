package messages

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Thread is where a request sits in its conversation: the first request starts one, and every later
// request continues the reply it names, in `diagnostics.previous_message_id`. Claude Code sends that in
// every mode; the `thread` object beside it only when the account's flags turn threads on, and then a
// continuation's `messages` holds only the new turn. So this, not the message count, is the position.
type Thread struct {
	Continue bool
	Previous string // the reply continued; empty exactly when !Continue
}

// Result is one tool_result block of the current turn: its text, and whether the tool failed. The
// tool_use id is not here: it differs every run.
type Result struct {
	Text    string
	IsError bool
}

// Key is what identifies a request among recorded ones (PROJECT.md "Recordings"): the thread, the
// current turn's prompt text, and its tool results. Nothing else of the body is read: the system prompt,
// system-role messages, metadata, model, tools and the rest differ between runs and Claude Code versions.
type Key struct {
	Thread   Thread
	LastText string
	Results  []Result
}

// ID is the key as one comparable string.
func (k Key) ID() string {
	b, _ := json.Marshal(k)
	return string(b)
}

var reminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// stripped is t without its system reminders (Claude Code appends them to prompts and tool results,
// and their text, such as a token countdown, varies), and without surrounding space.
func stripped(t string) string { return strings.TrimSpace(reminder.ReplaceAllString(t, "")) }

// ReadKey is the one reader of a Messages body's key. The error says why a body has none: it is not a
// Messages request, it carries no usable `diagnostics.previous_message_id`, or it has no user message.
func ReadKey(raw []byte) (Key, error) {
	var body struct {
		Diagnostics *struct {
			Previous json.RawMessage `json:"previous_message_id"`
		} `json:"diagnostics"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := decodeObject(raw, &body); err != nil {
		return Key{}, fmt.Errorf("body is not a Messages request: %w", err)
	}
	k := Key{}
	switch {
	case body.Diagnostics == nil || body.Diagnostics.Previous == nil:
		return Key{}, errors.New("the request carries no diagnostics.previous_message_id")
	case absent(body.Diagnostics.Previous):
	default:
		var previous string
		if json.Unmarshal(body.Diagnostics.Previous, &previous) != nil || previous == "" {
			return Key{}, errors.New("diagnostics.previous_message_id: expected null or a non-empty string")
		}
		k.Thread = Thread{Continue: true, Previous: previous}
	}
	content, found := lastUserContent(body.Messages)
	if !found {
		return Key{}, errors.New("messages: has no user message")
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		k.LastText = stripped(text)
		return k, nil
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"is_error"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return Key{}, errors.New("messages: the last user message's content is neither a string nor a list of blocks")
	}
	for _, b := range blocks {
		switch b.Type {
		case textType:
			if s := stripped(b.Text); s != "" {
				k.LastText = s
			}
		case toolResultType:
			k.Results = append(k.Results, Result{Text: stripped(strings.Join(contentTexts(b.Content), "\n")), IsError: b.IsError})
		}
	}
	return k, nil
}
