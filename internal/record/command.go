package record

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/http"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/cmdline"
	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/recording"
	"github.com/promptctl/mock-anthropic/internal/serve"
	"github.com/promptctl/mock-anthropic/internal/server"
)

// Command is record: the mock's listener, answering by forwarding. upstream is the transport that reaches
// the real hosts; main passes Transport().
func Command(stdout io.Writer, stderr *command.Log, upstream http.RoundTripper) command.Command {
	return command.Command{
		Kind:  command.Service,
		Usage: "--dir DIR [--port N] [--account FILE]",
		Run: func(ctx context.Context, args []string, crash func(error), learn func(command.Facts)) (command.Result, error) {
			c, err := parse(args)
			if err != nil {
				return command.Result{}, err
			}
			w, err := recording.NewWriter(c.dir)
			if err != nil {
				return command.Result{}, err
			}
			totals := &Totals{}
			rc := &Recorder{Upstream: upstream, Writer: w, Account: c.account, Totals: totals}
			result, err := serve.Launch(ctx, stdout, stderr, crash, learn, server.Options{
				Port:   c.port,
				Table:  endpoint.For(c.account, nil),
				Handle: rc.Handle,
			}, command.Facts{"dir": c.dir, "account_file": c.accountFile})
			if err != nil {
				return result, err
			}
			result.Facts = command.Facts{"saved": totals.Saved.Load(), "skipped": totals.Skipped.Load(), "refused": totals.Refused.Load(), "failed": totals.Failed.Load()}
			if totals.Refused.Load() > 0 || totals.Failed.Load() > 0 {
				result.Answer = command.No
			}
			return result, nil
		},
	}
}

type config struct {
	dir         string
	port        int
	accountFile *string
	account     account.Account
}

// [LAW:parse-dont-validate] parse is the one checkpoint record's command line crosses.
func parse(args []string) (config, error) {
	dir, port, accountFlag := &cmdline.Once{}, &cmdline.Once{}, &cmdline.Once{}
	rest, err := cmdline.Flags("record", func(fs *flag.FlagSet) {
		fs.Var(dir, "dir", "")
		fs.Var(port, "port", "")
		fs.Var(accountFlag, "account", "")
	}, args)
	if err != nil {
		return config{}, err
	}
	if len(rest) > 0 {
		return config{}, &command.UsageError{Why: "unexpected argument: " + rest[0]}
	}
	if dir.Value == nil {
		return config{}, &command.UsageError{Why: "--dir is required"}
	}
	c := config{account: account.Default()}
	if c.dir, err = cmdline.Directory("--dir", *dir.Value); err != nil {
		return config{}, err
	}
	if port.Value != nil {
		if c.port, err = cmdline.Port("--port", *port.Value, 0); err != nil {
			return config{}, err
		}
	}
	if accountFlag.Value != nil {
		path, err := cmdline.Absolute("--account", *accountFlag.Value)
		if err != nil {
			return config{}, err
		}
		a, err := account.ReadFile(path)
		var input *account.InputError
		if errors.As(err, &input) {
			return config{}, &command.UsageError{Why: input.Error()}
		}
		if err != nil {
			return config{}, err
		}
		c.accountFile, c.account = &path, a
	}
	return c, nil
}
