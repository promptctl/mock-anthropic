// Package coverage is the `coverage` command (PROJECT.md "Endpoint table"): every endpoint of the table
// as answered or out of scope, and a "no" when an in-scope endpoint has no generator.
package coverage

import (
	"context"
	"fmt"
	"io"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
)

// Command is coverage over the mock's own table, listing on stdout.
func Command(stdout io.Writer) command.Command {
	return Over(endpoint.For(account.Default()), stdout)
}

// Over is coverage over table; Command is the one caller outside tests.
func Over(table *endpoint.Table, stdout io.Writer) command.Command {
	return command.Command{
		Kind:  command.Task,
		Usage: "",
		Run: func(_ context.Context, args []string, _ func(error), _ func(command.Facts)) (command.Result, error) {
			if len(args) > 0 {
				return command.Result{}, &command.UsageError{Why: "unexpected argument: " + args[0]}
			}
			var answered, outOfScope, gaps []string
			for _, e := range table.Endpoints() {
				line := fmt.Sprintf("%s %s%s", e.Method(), e.Host(), e.Path())
				var label string
				switch reason, refused := e.OutOfScopeReason(); {
				case refused:
					label = fmt.Sprintf("%-14s %s  (%s): %s", "OUT OF SCOPE", line, e.ID(), reason)
					outOfScope = append(outOfScope, string(e.ID()))
				case hasGenerator(e):
					label = fmt.Sprintf("%-14s %s  (%s)", "ANSWERED", line, e.ID())
					answered = append(answered, string(e.ID()))
				default:
					label = fmt.Sprintf("%-14s %s  (%s)", "GAP", line, e.ID())
					gaps = append(gaps, string(e.ID()))
				}
				if _, err := fmt.Fprintln(stdout, label); err != nil {
					return command.Result{}, fmt.Errorf("writing the listing: %w", err)
				}
			}
			// [LAW:nothing-unseen] The counts and the gaps themselves are on the record, not only in the listing.
			facts := command.Facts{"answered": len(answered), "out_of_scope": len(outOfScope), "gaps": append([]string{}, gaps...)}
			if len(gaps) > 0 {
				return command.Result{Answer: command.No, Facts: facts}, nil
			}
			return command.Result{Answer: command.OK, Facts: facts}, nil
		},
	}
}

func hasGenerator(e endpoint.Endpoint) bool {
	_, ok := e.Generator()
	return ok
}
