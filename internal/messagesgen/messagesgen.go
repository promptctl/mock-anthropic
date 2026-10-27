// Package messagesgen is the generator for `POST /v1/messages` (PROJECT.md "Messages"). [LAW:effects-at-boundaries]
// The one place a Messages request meets the scripts: it reads the consumer's directories (the effect),
// parses the body, and renders the first reply that answers.
package messagesgen

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/messages"
	"github.com/promptctl/mock-anthropic/internal/recording"
	"github.com/promptctl/mock-anthropic/internal/script"
)

// New is the generator over the consumer's script directories, then the recordings, then the built-in
// answers. a is the account a request is scrubbed as before it is compared with a recording.
// [LAW:dataflow-not-control-flow] The scripts are read first, whatever the body: a script that cannot
// be read or is not a script is an error out of script.InOrder, which dispatch turns into the 500 and
// FAILED line naming its path, on every Messages request (PROJECT.md "A bad script").
func New(a account.Account, dirs []string, recorded, builtIns []script.Script) generator.Func {
	seen := &occurrences{count: map[[sha256.Size]byte]int{}}
	// Only a mock with recordings compares requests by key, so only it scrubs and reads every body.
	canonicalize := messages.Unkeyed
	if len(recorded) > 0 {
		canonicalize = func(body []byte) (messages.Canonical, error) { return recording.Canonicalize(body, a) }
	}
	return func(ctx context.Context, _ map[string]string, r generator.Request) (generator.Answer, error) {
		scripts, err := script.InOrder(dirs, recorded, builtIns)
		if err != nil {
			return generator.Answer{}, err
		}
		request, err := messages.ParseRequest(r.Body, canonicalize)
		var failed *messages.CanonicalizeError
		if errors.As(err, &failed) {
			return generator.Answer{}, err
		}
		if err != nil {
			// No script was matched against, so there is nothing to report beyond the why.
			return generator.Answer{Reply: generator.Declined{Why: err.Error()}}, nil
		}
		// [LAW:nothing-unseen] The key the request was compared by, or why it has none, rides on every answer.
		keyFacts := request.Canonical.Facts()
		// [LAW:nothing-unseen] Each answer carries its facts: the script that answered and what was sent,
		// or every script tried. "0 tried" is a request that found no scripts at all.
		switch selection := script.FirstReply(scripts, request).(type) {
		case script.Matched:
			response, sent, err := render(ctx, selection.Reply, request, seen)
			if err != nil {
				return generator.Answer{}, err
			}
			facts := generator.Facts{"script": selection.Origin, "reply": sent}
			if recorded, ok := selection.Reply.(script.RecordedReply); ok {
				facts["recording"] = recorded.File
			}
			maps.Copy(facts, keyFacts)
			return generator.Answer{Reply: response, Facts: facts}, nil
		case script.Unmatched:
			return generator.Answer{
				Reply: generator.Declined{Why: fmt.Sprintf("no script matches: %d tried", len(selection.Tried))},
				Facts: generator.Facts{"scripts_tried": selection.Tried, "key": keyFacts["key"]},
			}, nil
		default:
			return generator.Answer{}, fmt.Errorf("script selection of type %T is neither matched nor unmatched", selection)
		}
	}
}

// occurrences counts how many times each request body has been answered with a message, so identical
// bodies (the quota probe every session, parallel subagents) get distinct ids that a replay of the same
// request sequence still reproduces.
type occurrences struct {
	mu    sync.Mutex
	count map[[sha256.Size]byte]int
}

func (o *occurrences) next(body string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := sha256.Sum256([]byte(body))
	n := o.count[key]
	o.count[key] = n + 1
	return n
}
