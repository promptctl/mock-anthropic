package recording

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/messages"
	"github.com/promptctl/mock-anthropic/internal/script"
)

// Canonical forms of a request body (PROJECT.md "Recordings", Matching): HasKey, or NoKey with the
// reason the body has none. The fields are unexported, so only Canonicalize makes one.
type (
	HasKey struct{ key messages.Key }
	NoKey  struct{ why string }
)

// maxText is the longest prompt text a record carries; the rest is in the key's digest.
const maxText = 200

func (h HasKey) Facts() map[string]any {
	k := h.key
	digest := sha256.Sum256([]byte(k.ID()))
	results := make([]map[string]any, 0, len(k.Results))
	for _, r := range k.Results {
		sum := sha256.Sum256([]byte(r.Text))
		results = append(results, map[string]any{"sha256": hex.EncodeToString(sum[:8]), "is_error": r.IsError})
	}
	last := k.LastText
	if len(last) > maxText {
		last = strings.ToValidUTF8(last[:maxText], "")
	}
	key := map[string]any{"sha256": hex.EncodeToString(digest[:8]), "last_text": last, "results": results}
	if k.Thread.Continue {
		key["thread"], key["previous"] = "continue", k.Thread.Previous
	} else {
		key["thread"] = "create"
	}
	return map[string]any{"key": key}
}

func (n NoKey) Facts() map[string]any { return map[string]any{"key": "none: " + n.why} }

// Canonicalize is the only way from a request body to its canonical form: scrub the body as a recording
// is scrubbed, then read its key. The scrub makes a live request comparable with a stored one (a
// recording holds scrubbed requests), and is idempotent, so a stored request canonicalizes to the key it
// was saved with. [LAW:single-enforcer] The error is a scrub that failed on a body that is a Messages
// request: the mock failing, never a miss.
func Canonicalize(raw []byte, a account.Account) (messages.Canonical, error) {
	scrubbed, err := scrubRequest(raw, a)
	if err != nil {
		return nil, err
	}
	key, err := messages.ReadKey(scrubbed)
	if err != nil {
		return NoKey{err.Error()}, nil
	}
	return HasKey{key}, nil
}

// Scripts is the recordings in dirs as one script, ahead of the built-in answers in script.InOrder.
// Every exchange is canonicalized by the function a live request is, and one with no key is refused,
// naming its file. The first exchange with a key answers it; the files of the others are returned as
// shadowed, since a conversation whose first request is shadowed cannot be reached.
func Scripts(dirs []string, a account.Account) (script.Script, []string, error) {
	byKey := map[string]script.RecordedReply{}
	var shadowed []string
	for _, dir := range dirs {
		exchanges, err := Load(dir)
		if err != nil {
			return script.Script{}, nil, err
		}
		for _, e := range exchanges {
			c, err := Canonicalize(e.Request, a)
			if err != nil {
				return script.Script{}, nil, fmt.Errorf("%s: %w", e.File, err)
			}
			h, ok := c.(HasKey)
			if !ok {
				return script.Script{}, nil, fmt.Errorf("%s: the request has no key: %s", e.File, strings.TrimPrefix(fmt.Sprint(c.Facts()["key"]), "none: "))
			}
			id := h.key.ID()
			if _, dup := byKey[id]; dup {
				shadowed = append(shadowed, e.File)
				continue
			}
			// Parse checked the reply to be a message.
			reply, _ := script.ParseReply(e.Reply)
			byKey[id] = script.RecordedReply{MessageReply: reply.(script.MessageReply), ID: e.MessageID, File: e.File}
		}
	}
	sort.Strings(shadowed)
	return script.Script{Origin: "recordings", Answer: func(r messages.Request) script.Reply {
		h, ok := r.Canonical.(HasKey)
		if !ok {
			return nil
		}
		e, ok := byKey[h.key.ID()]
		if !ok {
			return nil
		}
		return e
	}}, shadowed, nil
}
