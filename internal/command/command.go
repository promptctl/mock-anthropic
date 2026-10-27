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
	"os/signal"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
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

// Kind is how a command ends. A task finishes by itself: a signal cancels its context and ends the
// invocation at once, without waiting for it. A service runs until its context is cancelled and then
// stops, returning a nil error for a clean stop; while it stops, a second signal ends the invocation
// without it.
type Kind int

// A Kind's zero value is no kind, so a command registered without one fails rather than run as a Task.
const (
	_ Kind = iota
	Task
	Service
)

type Command struct {
	Kind  Kind
	Usage string
	// Run returns a UsageError for a wrong command line; any other error fails the invocation. crash
	// is how work Run starts outside its own goroutine reports an error nobody caught there: it ends
	// the invocation as a crash, as a panic in Run does, except that a Service is first stopped, so
	// what it learned stays on the record. learn is how a command puts what it has learned on the record
	// before it returns: a second signal ends the invocation without waiting for Run, and the record then
	// carries what was learned by then. Facts learned later replace earlier ones of the same name.
	Run func(ctx context.Context, args []string, crash func(error), learn func(Facts)) (Result, error)
}

type Commands map[string]Command

// UsageError is a wrong command line, which the dispatcher answers with exit 2.
type UsageError struct{ Why string }

func (e *UsageError) Error() string { return e.Why }

// Signals are the signals that ask an invocation to end, by the names its record gives them.
var Signals = map[os.Signal]string{syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGTERM: "SIGTERM"}

// Notify delivers every one of the Signals to a channel, the one the process hands Main. SIGPIPE is
// ignored, so a write to a closed stdout or stderr fails as an error the command reports, instead of
// killing the process before it writes its record.
func Notify() <-chan os.Signal {
	signal.Ignore(syscall.SIGPIPE)
	signals := make(chan os.Signal, len(Signals))
	for sig := range Signals {
		signal.Notify(signals, sig)
	}
	return signals
}

// Record is the one record of an invocation. EndedBy is the signal that first asked it to end, "crash"
// for a panic nobody recovered, a Run that never returned, or an error reported through crash, or nil
// when its command finished by itself.
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

// Log is an invocation's stderr. [LAW:single-enforcer] Every line the invocation writes, its command's
// records included, goes through it whole, one at a time, and the invocation's record is the last:
// once it is out, a write fails with ErrEnded. A record still waiting when a second signal ends the
// invocation at once is one of those, and the invocation's record says so.
type Log struct {
	mu    sync.Mutex
	w     io.Writer
	ended bool
}

// ErrEnded is a write to a Log after the invocation's record.
var ErrEnded = errors.New("the invocation has ended; its record is written")

func NewLog(w io.Writer) *Log { return &Log{w: w} }

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return 0, ErrEnded
	}
	return l.w.Write(p)
}

func (l *Log) end(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ended = true
	fmt.Fprintln(l.w, line)
}

// Main runs argv's command and writes the invocation's one record on stderr, however it ends, returning
// the exit code that record carries. signals delivers the Signals as they arrive.
func Main(commands Commands, argv []string, stderr *Log, signals <-chan os.Signal) int {
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
		why := []string{fmt.Sprintf("recording facts: %v", err)}
		if record.Why != "" {
			why = append([]string{record.Why}, why...)
		}
		record.Outcome, record.Exit, record.Why, record.Facts = Failed, ExitCode(Failed), strings.Join(why, "\n"), Facts{}
		line, _ = json.Marshal(record)
	}
	stderr.end(strings.ToUpper(string(record.Outcome)) + " " + string(line))
	return record.Exit
}

type ending struct {
	Outcome Outcome
	Why     string
	Facts   Facts
}

// end is one thing that asked the invocation to end: a signal by name, or "crash" with what crashed.
type end struct {
	name  string
	crash string
}

// learnedFacts is what a running command has told the dispatcher so far, from any goroutine.
type learnedFacts struct {
	mu    sync.Mutex
	facts Facts
}

func (l *learnedFacts) add(f Facts) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.facts == nil {
		l.facts = Facts{}
	}
	for k, v := range f {
		l.facts[k] = v
	}
}

func (l *learnedFacts) all() Facts {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := Facts{}
	for k, v := range l.facts {
		out[k] = v
	}
	return out
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
	if cmd.Kind != Task && cmd.Kind != Service {
		return failed(nil, fmt.Errorf("command %s is registered with no Kind", argv[0]), Facts{}), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// [LAW:single-enforcer] A reported crash is one more end on this loop, never a second way out.
	crashes := make(chan error)
	ended := make(chan struct{})
	defer close(ended)
	crash := func(err error) {
		if err == nil {
			err = errors.New("a crash was reported with no error")
		}
		select {
		case crashes <- err:
		case <-ended: // the invocation has its record already; nothing is left to end
		}
	}
	var learned learnedFacts
	done := make(chan settled, 1)
	go func() {
		returned := false
		defer func() {
			if p := recover(); p != nil {
				done <- settled{crash: fmt.Sprintf("panic: %v\n%s", p, debug.Stack())}
			} else if !returned {
				done <- settled{crash: "the command exited without returning (runtime.Goexit)"}
			}
		}()
		result, err := cmd.Run(ctx, argv[1:], crash, learned.add)
		returned = true
		done <- settled{result: result, err: err}
	}()

	var ends []end
	signalled := 0
	for {
		select {
		case sig := <-signals:
			if len(done) > 0 { // the command finished before the signal could end it; its ending stands
				continue
			}
			ends, signalled = append(ends, end{name: Signals[sig]}), signalled+1
			cancel()
			if cmd.Kind == Task || signalled > 1 {
				return failed(ends, nil, learned.all()), ends
			}
		case err := <-crashes:
			// A sink that keeps failing reports the same crash from every record its stop writes; once is
			// the fact.
			if !slices.Contains(ends, end{name: "crash", crash: err.Error()}) {
				ends = append(ends, end{name: "crash", crash: err.Error()})
			}
			cancel()
			// A crash never cuts a stop short: a stopping Service still returns, so what it learned stays
			// on the record; a Task that already finished fails with its facts.
			if cmd.Kind == Task && len(done) == 0 {
				return failed(ends, nil, learned.all()), ends
			}
		case s := <-done:
			if s.crash != "" {
				ends = append(ends, end{name: "crash", crash: s.crash})
				return failed(ends, nil, learned.all()), ends
			}
			var usageErr *UsageError
			switch {
			case s.err != nil && len(ends) == 0 && errors.As(s.err, &usageErr):
				return usage(commands, stderr, usageErr.Why), ends
			case s.err != nil || slices.ContainsFunc(ends, func(e end) bool { return e.crash != "" }):
				return failed(ends, s.err, s.result.Facts), ends
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
