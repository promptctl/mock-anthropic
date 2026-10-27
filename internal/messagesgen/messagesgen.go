// Package messagesgen is the generator for `POST /v1/messages` (PROJECT.md "Messages"). [LAW:effects-at-boundaries]
// The one place a Messages request meets the scripts: it reads the consumer's directories (the effect),
// parses the body, and renders the first reply that answers.
package messagesgen

import (
	"context"
	"fmt"

	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/messages"
	"github.com/promptctl/mock-anthropic/internal/script"
)

// New is the generator over the consumer's script directories, then the built-in answers.
// [LAW:dataflow-not-control-flow] The scripts are read first, whatever the body: a script that cannot
// be read or is not a script is an error out of script.InOrder, which dispatch turns into the 500 and
// FAILED line naming its path, on every Messages request (PROJECT.md "A bad script").
func New(dirs []string, builtIns []script.Script) generator.Func {
	return func(ctx context.Context, _ map[string]string, r generator.Request) (generator.Answer, error) {
		scripts, err := script.InOrder(dirs, builtIns)
		if err != nil {
			return generator.Answer{}, err
		}
		request, err := messages.ParseRequest(r.Body)
		if err != nil {
			// No script was matched against, so there is nothing to report beyond the why.
			return generator.Answer{Reply: generator.Declined{Why: err.Error()}}, nil
		}
		// [LAW:nothing-unseen] Each answer carries its facts: the script that answered and what was sent,
		// or every script tried. "0 tried" is a request that found no scripts at all.
		switch selection := script.FirstReply(scripts, request).(type) {
		case script.Matched:
			response, sent, err := render(ctx, selection.Reply, request)
			if err != nil {
				return generator.Answer{}, err
			}
			return generator.Answer{Reply: response, Facts: generator.Facts{"script": selection.Origin, "reply": sent}}, nil
		case script.Unmatched:
			return generator.Answer{
				Reply: generator.Declined{Why: fmt.Sprintf("no script matches: %d tried", len(selection.Tried))},
				Facts: generator.Facts{"scripts_tried": selection.Tried},
			}, nil
		default:
			return generator.Answer{}, fmt.Errorf("script selection of type %T is neither matched nor unmatched", selection)
		}
	}
}
