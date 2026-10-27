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
	"regexp"
	"strconv"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/account"
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
		Run: func(ctx context.Context, args []string, crash func(error)) (command.Result, error) {
			c, err := parse(args)
			if err != nil {
				return command.Result{}, err
			}
			executable, err := os.Executable()
			if err != nil {
				return command.Result{}, fmt.Errorf("finding the certificates directory: %w", err)
			}
			s, err := server.Start(server.Options{
				Port:   c.port,
				Table:  endpoint.Default,
				Handle: dispatch.Dispatch,
				// Beside the binary: built at the repository root, the gitignored /certs/, so every run of
				// one build presents the CA a consumer already trusts.
				CertsDir: filepath.Join(filepath.Dir(executable), "certs"),
				Emit:     Sink(stderr, crash),
			})
			if err != nil {
				return command.Result{}, err
			}
			certs := s.Certificates()
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
			// [LAW:nothing-unseen] Which account won (null: the default), and where each certificate came
			// from. What ended the run is the dispatcher's to record.
			return command.Result{Answer: command.OK, Facts: command.Facts{
				"port":         s.Port(),
				"ca":           certs.CA.CertPath,
				"account_file": c.accountFile,
				"scenarios":    c.scenarios,
				"certificates": map[string]string{"ca": string(certs.CA.Outcome), "leaf": string(certs.Leaf.Outcome)},
			}}, nil
		},
	}
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
	account     account.Account // checked now; the generators that answer with it are later work
}

var portText = regexp.MustCompile(`^\d{1,5}$`)

// once is a flag that takes one value; given twice, it is a wrong command line.
type once struct {
	value *string
	name  string
}

func (o *once) String() string { return "" }
func (o *once) Set(v string) error {
	if o.value != nil {
		return fmt.Errorf("--%s: given more than once", o.name)
	}
	o.value = &v
	return nil
}

type list []string

func (l *list) String() string     { return "" }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// [LAW:parse-dont-validate] parse is the one checkpoint serve's command line crosses; every failure
// is a UsageError naming its cause.
func parse(args []string) (config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	port, accountFlag := &once{name: "port"}, &once{name: "account"}
	var scenarios list
	fs.Var(port, "port", "")
	fs.Var(accountFlag, "account", "")
	fs.Var(&scenarios, "scenarios", "")
	if err := fs.Parse(args); err != nil {
		// flag names a flag by one dash; PROJECT.md and the usage line name every flag by two.
		why, unknown := strings.CutPrefix(err.Error(), "flag provided but not defined: -")
		if unknown {
			why = "unknown flag: --" + strings.TrimPrefix(why, "-")
		} else {
			why = err.Error()
		}
		return config{}, &command.UsageError{Why: why}
	}
	if fs.NArg() > 0 {
		return config{}, &command.UsageError{Why: "unexpected argument: " + fs.Arg(0)}
	}
	c := config{scenarios: []string{}, account: account.Default()}
	if port.value != nil {
		n, err := strconv.Atoi(*port.value)
		if !portText.MatchString(*port.value) || err != nil || n > 65535 {
			return config{}, &command.UsageError{Why: fmt.Sprintf("--port %q: not a port number from 0 to 65535", *port.value)}
		}
		c.port = n
	}
	for _, dir := range scenarios {
		path, err := directory("--scenarios", dir)
		if err != nil {
			return config{}, err
		}
		c.scenarios = append(c.scenarios, path)
	}
	if accountFlag.value != nil {
		path, err := absolute("--account", *accountFlag.value)
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

// absolute is a command-line path made absolute, so a record names it whatever the reader's working
// directory. An empty one (an unset shell variable) is a wrong command line, not the working directory.
func absolute(given, text string) (string, error) {
	if text == "" {
		return "", &command.UsageError{Why: given + ": an empty path"}
	}
	path, err := filepath.Abs(text)
	if err != nil {
		return "", &command.UsageError{Why: fmt.Sprintf("%s %s: %v", given, text, err)}
	}
	return path, nil
}

// directory is a command-line path that must name a directory there now. Scripts are read per request,
// so one written after the start still answers; the directory itself must exist at the start.
func directory(given, text string) (string, error) {
	path, err := absolute(given, text)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", &command.UsageError{Why: fmt.Sprintf("%s %s: %v", given, path, errors.Unwrap(err))}
	}
	if !info.IsDir() {
		return "", &command.UsageError{Why: fmt.Sprintf("%s %s: not a directory", given, path)}
	}
	return path, nil
}
