// Package serve is PROJECT.md "Commands" `serve`: it starts the mock and runs until it is told to stop.
// Its one stdout line is how a consumer finds it; every record goes to stderr.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/cmdline"
	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/server"
)

// Command is serve, writing its ready line on stdout and its records on stderr.
func Command(stdout io.Writer, stderr *command.Log) command.Command {
	return command.Command{
		Kind:  command.Service,
		Usage: "[--port N] [--scenarios DIR]... [--account FILE]",
		Run: func(ctx context.Context, args []string, crash func(error), learn func(command.Facts)) (command.Result, error) {
			c, err := parse(args)
			if err != nil {
				return command.Result{}, err
			}
			return Launch(ctx, stdout, stderr, crash, learn, server.Options{
				Port:   c.port,
				Table:  endpoint.For(c.account, c.scenarios),
				Handle: dispatch.Dispatch,
			}, command.Facts{
				"account_file": c.accountFile,
				"scenarios":    c.scenarios,
			})
		},
	}
}

// Launch is what every command that runs the mock shares: start the server from o (Port, Table and
// Handle are the caller's; the certificates directory, the records and the crash path are Launch's),
// learn the facts of the start, announce it on stdout, and serve until ctx ends. facts are the
// caller's, learned beside the port and the certificates.
func Launch(ctx context.Context, stdout io.Writer, stderr *command.Log, crash func(error), learn func(command.Facts), o server.Options, facts command.Facts) (command.Result, error) {
	executable, err := os.Executable()
	if err == nil {
		// The build itself, not a symlink to it, so every way of reaching one build finds one CA.
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return command.Result{}, fmt.Errorf("finding the certificates directory: %w", err)
	}
	// Beside the binary: built at the repository root, the gitignored /certs/, so every run of one
	// build presents the CA a consumer already trusts.
	o.CertsDir = filepath.Join(filepath.Dir(executable), "certs")
	o.Emit, o.Crash = Sink(stderr, crash), crash
	s, err := server.Start(o)
	if err != nil {
		return command.Result{}, err
	}
	certs := s.Certificates()
	// [LAW:nothing-unseen] Which account won (null: the default), and where each certificate came
	// from. What ended the run is the dispatcher's to record. Learned before serving, so a second
	// signal at any point after this still finds them on the record. The dispatcher keeps what was
	// learned on every ending.
	facts["port"], facts["ca"] = s.Port(), certs.CA.CertPath
	facts["certificates"] = map[string]string{"ca": string(certs.CA.Outcome), "leaf": string(certs.Leaf.Outcome)}
	learn(facts)
	ready, err := json.Marshal(struct {
		Port int    `json:"port"`
		CA   string `json:"ca"`
	}{s.Port(), certs.CA.CertPath})
	if err == nil {
		_, err = fmt.Fprintf(stdout, "%s\n", ready)
	}
	if err != nil {
		// A consumer that never reads the ready line cannot find the mock; nothing is left to serve.
		s.Stop()
		return command.Result{}, fmt.Errorf("writing the ready line: %w", err)
	}
	<-ctx.Done()
	s.Stop()
	return command.Result{Answer: command.OK}, nil
}

// Sink writes each record as its line on the invocation's log, from any goroutine.
// [LAW:no-silent-failure] A record that cannot be written is reported as a crash, never dropped.
func Sink(log *command.Log, crash func(error)) func(server.Event) {
	return func(e server.Event) {
		line, err := server.Line(e)
		if err != nil {
			crash(fmt.Errorf("recording %T: %w", e, err))
			return
		}
		_, err = fmt.Fprintln(log, line)
		// After the invocation's record nothing is left to end; its record already says what ended it.
		if err != nil && !errors.Is(err, command.ErrEnded) {
			crash(fmt.Errorf("writing a record: %w", err))
		}
	}
}

// config is serve's command line once parsed: every path absolute, every input checked.
type config struct {
	port        int
	scenarios   []string
	accountFile *string         // nil: the default account
	account     account.Account // checked now; the endpoint table answers as it
}

// [LAW:parse-dont-validate] parse is the one checkpoint serve's command line crosses; every failure
// is a UsageError naming its cause.
func parse(args []string) (config, error) {
	port, accountFlag := &cmdline.Once{}, &cmdline.Once{}
	var scenarios cmdline.List
	rest, err := cmdline.Flags("serve", func(fs *flag.FlagSet) {
		fs.Var(port, "port", "")
		fs.Var(accountFlag, "account", "")
		fs.Var(&scenarios, "scenarios", "")
	}, args)
	if err != nil {
		return config{}, err
	}
	if len(rest) > 0 {
		return config{}, &command.UsageError{Why: "unexpected argument: " + rest[0]}
	}
	c := config{scenarios: []string{}, account: account.Default()}
	if port.Value != nil {
		n, err := cmdline.Port("--port", *port.Value, 0)
		if err != nil {
			return config{}, err
		}
		c.port = n
	}
	for _, dir := range scenarios {
		// Scripts are read per request, so one written after the start still answers; the directory
		// itself must exist at the start.
		path, err := cmdline.Directory("--scenarios", dir)
		if err != nil {
			return config{}, err
		}
		c.scenarios = append(c.scenarios, path)
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
