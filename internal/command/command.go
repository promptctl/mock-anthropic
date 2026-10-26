// Package command is the dispatcher every command runs under (PROJECT.md "Commands"). It owns what a
// command does not: the invocation's lifetime, its one record, and its exit code.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Facts are what a command learned, carried on its record.
type Facts map[string]any

// Outcome is how an invocation ended; its capitals open the record's line.
type Outcome string

const (
	OK     Outcome = "ok"
	No     Outcome = "no"
	Usage  Outcome = "usage"
	Failed Outcome = "failed"
)

// Result is what a command returns: its answer, yes or no, and what it learned on the way.
type Result struct {
	Answer Outcome // OK or No
	Facts  Facts
}

// Kind is how a command ends. A task finishes by itself and never hears of a signal: one ends the
// invocation at once, without waiting for it. A service runs until its context is cancelled and then
// stops; while it stops, a second signal ends the invocation without it.
type Kind int

const (
	Task Kind = iota
	Service
)

type Command struct {
	Kind  Kind
	Usage string
	// Run returns a UsageError for a wrong command line; any other error fails the invocation.
	Run func(ctx context.Context, args []string) (Result, error)
}

type Commands map[string]Command

// UsageError is a wrong command line, which the dispatcher answers with exit 2.
type UsageError struct{ Why string }

func (e *UsageError) Error() string { return e.Why }

// Signals are the signals that ask an invocation to end, by the names its record gives them.
var Signals = map[os.Signal]string{syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGTERM: "SIGTERM"}

// Record is the one record of an invocation. EndedBy is the signal that first asked it to end, "crash"
// for a panic nobody recovered, or nil when its command finished by itself.
type Record struct {
	Event      string   `json:"event"`
	Argv       []string `json:"argv"`
	DurationMS int64    `json:"duration_ms"`
	Exit       int      `json:"exit"`
	EndedBy    *string  `json:"ended_by"`
	Outcome    Outcome  `json:"outcome"`
	Why        string   `json:"why,omitempty"`
	Facts      Facts    `json:"facts"`
}

// ExitCode is the one statement of PROJECT.md's exit codes: 0 success, 1 the answer is "no" or the
// mock failed, 2 a wrong command line.
func ExitCode(outcome Outcome) int {
	switch outcome {
	case OK:
		return 0
	case Usage:
		return 2
	default:
		return 1
	}
}

// Main runs argv's command and writes the invocation's one record on stderr, however it ends, returning
// the exit code that record carries. signals delivers the Signals as they arrive.
func Main(commands Commands, argv []string, stderr io.Writer, signals <-chan os.Signal) int {
	started := time.Now()
	ending, ends := invoke(commands, argv, stderr, signals)
	record := Record{
		Event:      "command",
		Argv:       append([]string{}, argv...),
		DurationMS: time.Since(started).Milliseconds(),
		Exit:       ExitCode(ending.Outcome),
		Outcome:    ending.Outcome,
		Why:        ending.Why,
		Facts:      ending.Facts,
	}
	if record.Facts == nil {
		record.Facts = Facts{}
	}
	if len(ends) > 0 {
		record.EndedBy = &ends[0].name
	}
	line, err := json.Marshal(record)
	if err != nil {
		// Facts that cannot be JSON are the mock's own failure; the record still goes out, without them.
		record.Outcome, record.Exit, record.Why, record.Facts = Failed, 1, fmt.Sprintf("%s\nrecording facts: %v", record.Why, err), Facts{}
		line, _ = json.Marshal(record)
	}
	fmt.Fprintf(stderr, "%s %s\n", strings.ToUpper(string(record.Outcome)), line)
	return record.Exit
}

type ending struct {
	Outcome Outcome
	Why     string
	Facts   Facts
}

// end is one thing that asked the invocation to end: a signal by name, or "crash" with its panic.
type end struct {
	name  string
	crash string
}

type settled struct {
	result Result
	err    error
	crash  string // set when run panicked
}

func invoke(commands Commands, argv []string, stderr io.Writer, signals <-chan os.Signal) (ending, []end) {
	if len(argv) == 0 {
		return usage(commands, stderr, "no command given"), nil
	}
	cmd, found := commands[argv[0]]
	if !found {
		return usage(commands, stderr, "unknown command: "+argv[0]), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan settled, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- settled{crash: fmt.Sprintf("panic: %v\n%s", p, debug.Stack())}
			}
		}()
		result, err := cmd.Run(ctx, argv[1:])
		done <- settled{result: result, err: err}
	}()

	var ends []end
	for {
		select {
		case sig := <-signals:
			ends = append(ends, end{name: Signals[sig]})
			cancel()
			if cmd.Kind == Task || len(ends) > 1 {
				return failed(ends, nil, Facts{}), ends
			}
		case s := <-done:
			if s.crash != "" {
				ends = append(ends, end{name: "crash", crash: s.crash})
				return failed(ends, nil, Facts{}), ends
			}
			var usageErr *UsageError
			switch {
			case s.err != nil && len(ends) == 0 && errors.As(s.err, &usageErr):
				return usage(commands, stderr, usageErr.Why), ends
			case s.err != nil:
				return failed(ends, s.err, Facts{}), ends
			case s.result.Answer != OK && s.result.Answer != No:
				return failed(ends, fmt.Errorf("command answered %q, not ok or no", s.result.Answer), s.result.Facts), ends
			}
			return ending{Outcome: s.result.Answer, Facts: s.result.Facts}, ends
		}
	}
}

// failed tells every end, in order: the first asked the command to finish, every later one came while
// it was ending; then the command's own error, if it had one.
func failed(ends []end, err error, facts Facts) ending {
	var why []string
	for i, e := range ends {
		when := "while the command was ending"
		if i == 0 {
			when = "before the command finished"
		}
		if e.crash != "" {
			why = append(why, when+": "+e.crash)
		} else {
			why = append(why, e.name+" "+when)
		}
	}
	if err != nil {
		why = append(why, err.Error())
	}
	return ending{Outcome: Failed, Why: strings.Join(why, "\n"), Facts: facts}
}

func usage(commands Commands, stderr io.Writer, why string) ending {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintf(stderr, "mock-anthropic: %s\nusage: mock-anthropic <command> [args]\n", why)
	for _, name := range names {
		fmt.Fprintln(stderr, strings.TrimRight("  "+name+" "+commands[name].Usage, " "))
	}
	return ending{Outcome: Usage, Why: why, Facts: Facts{}}
}
