package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// run dispatches argv over commands and returns the exit code, the record read back from the last
// stderr line, and every stderr line. The line must open with the record's outcome in capitals.
func run(t *testing.T, commands Commands, argv []string, signals <-chan os.Signal) (int, map[string]any, []string) {
	t.Helper()
	var stderr bytes.Buffer
	exit := Main(commands, argv, NewLog(&stderr), signals)
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	head, body, _ := strings.Cut(last, " ")
	var record map[string]any
	if err := json.Unmarshal([]byte(body), &record); err != nil {
		t.Fatalf("last stderr line is not a record: %q: %v", last, err)
	}
	if head != strings.ToUpper(record["outcome"].(string)) {
		t.Fatalf("line opens with %q, record outcome is %q", head, record["outcome"])
	}
	return exit, record, lines
}

func expect(t *testing.T, record map[string]any, want map[string]any) {
	t.Helper()
	for key, value := range want {
		got, _ := json.Marshal(record[key])
		wanted, _ := json.Marshal(value)
		if string(got) != string(wanted) {
			t.Errorf("record %s = %s, want %s (record %v)", key, got, wanted, record)
		}
	}
}

func task(run func(ctx context.Context, args []string) (Result, error)) Commands {
	return Commands{"stand_in": {Kind: Task, Run: func(ctx context.Context, args []string, _ func(error)) (Result, error) { return run(ctx, args) }}}
}

func TestWrongCommandLine(t *testing.T) {
	commands := Commands{"serve": {Usage: "[--port N]"}}
	for _, tc := range []struct {
		what string
		argv []string
		why  string
	}{
		{"no command", []string{}, "no command given"},
		{"an unknown command", []string{"frobnicate"}, "unknown command: frobnicate"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			exit, record, lines := run(t, commands, tc.argv, nil)
			if exit != 2 {
				t.Errorf("exit %d, want 2", exit)
			}
			expect(t, record, map[string]any{"event": "command", "outcome": "usage", "exit": 2, "argv": tc.argv, "ended_by": nil, "why": tc.why, "facts": map[string]any{}})
			if !strings.Contains(strings.Join(lines, "\n"), "  serve [--port N]") {
				t.Errorf("usage does not list serve: %q", lines)
			}
		})
	}
}

func TestAnswers(t *testing.T) {
	for _, tc := range []struct {
		answer Outcome
		exit   int
	}{{OK, 0}, {No, 1}} {
		exit, record, _ := run(t, task(func(context.Context, []string) (Result, error) {
			return Result{Answer: tc.answer, Facts: Facts{"learned": "this"}}, nil
		}), []string{"stand_in"}, nil)
		if exit != tc.exit {
			t.Errorf("%s: exit %d, want %d", tc.answer, exit, tc.exit)
		}
		expect(t, record, map[string]any{"outcome": tc.answer, "exit": tc.exit, "ended_by": nil, "facts": map[string]any{"learned": "this"}})
		if _, ok := record["duration_ms"].(float64); !ok {
			t.Errorf("no duration_ms: %v", record)
		}
	}
}

func TestCommandUsageError(t *testing.T) {
	exit, record, _ := run(t, task(func(_ context.Context, args []string) (Result, error) {
		return Result{}, &UsageError{Why: "unexpected argument: " + args[0]}
	}), []string{"stand_in", "extra"}, nil)
	if exit != 2 {
		t.Errorf("exit %d, want 2", exit)
	}
	expect(t, record, map[string]any{"outcome": "usage", "why": "unexpected argument: extra", "argv": []string{"stand_in", "extra"}})
}

func TestCommandError(t *testing.T) {
	exit, record, _ := run(t, task(func(context.Context, []string) (Result, error) {
		return Result{Facts: Facts{"removed": 1}}, errors.New("the command broke")
	}), []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "why": "the command broke", "ended_by": nil, "facts": map[string]any{"removed": 1}})
}

func TestAnswerNeitherOKNorNo(t *testing.T) {
	exit, record, _ := run(t, task(func(context.Context, []string) (Result, error) {
		return Result{Answer: Usage, Facts: Facts{"kept": true}}, nil
	}), []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "why": `command answered "usage", not ok or no`, "facts": map[string]any{"kept": true}})
}

func TestFactsThatAreNotJSON(t *testing.T) {
	exit, record, _ := run(t, task(func(context.Context, []string) (Result, error) {
		return Result{Answer: OK, Facts: Facts{"x": math.NaN()}}, nil
	}), []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "exit": 1, "why": "recording facts: json: unsupported value: NaN", "facts": map[string]any{}})
}

func TestGoexitIsACrash(t *testing.T) {
	exit, record, _ := run(t, task(func(context.Context, []string) (Result, error) {
		runtime.Goexit()
		return Result{}, nil
	}), []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "ended_by": "crash", "why": "before the command finished: the command exited without returning (runtime.Goexit)"})
}

func TestPanicIsACrash(t *testing.T) {
	exit, record, _ := run(t, task(func(context.Context, []string) (Result, error) { panic("nobody caught this") }), []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "ended_by": "crash"})
	if why := record["why"].(string); !strings.Contains(why, "nobody caught this") || !strings.Contains(why, "command_test.go") {
		t.Errorf("why lacks the panic and its stack: %q", why)
	}
}

// Each of PROJECT.md's signals, listed here so a signal the dispatcher stops hearing fails its test.
func TestSignalEndsATaskAtOnce(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM} {
		name := Signals[sig]
		t.Run(name, func(t *testing.T) {
			signals := make(chan os.Signal, 1)
			stuck := task(func(context.Context, []string) (Result, error) {
				signals <- sig
				select {}
			})
			exit, record, _ := run(t, stuck, []string{"stand_in"}, signals)
			if exit != 1 {
				t.Errorf("exit %d, want 1", exit)
			}
			expect(t, record, map[string]any{"outcome": "failed", "ended_by": name, "why": name + " before the command finished"})
		})
	}
}

func TestServiceStopsOnASignal(t *testing.T) {
	signals := make(chan os.Signal, 1)
	service := Commands{"stand_in": {Kind: Service, Run: func(ctx context.Context, _ []string, _ func(error)) (Result, error) {
		signals <- syscall.SIGTERM
		<-ctx.Done()
		return Result{Answer: OK, Facts: Facts{"stopped": "cleanly"}}, nil
	}}}
	exit, record, _ := run(t, service, []string{"stand_in"}, signals)
	if exit != 0 {
		t.Errorf("exit %d, want 0", exit)
	}
	expect(t, record, map[string]any{"outcome": "ok", "ended_by": "SIGTERM", "facts": map[string]any{"stopped": "cleanly"}})
}

func TestSecondSignalEndsAStoppingService(t *testing.T) {
	signals := make(chan os.Signal, 2)
	service := Commands{"stand_in": {Kind: Service, Run: func(ctx context.Context, _ []string, _ func(error)) (Result, error) {
		signals <- syscall.SIGINT
		<-ctx.Done()
		signals <- syscall.SIGINT
		select {}
	}}}
	exit, record, _ := run(t, service, []string{"stand_in"}, signals)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "ended_by": "SIGINT", "why": "SIGINT before the command finished\nSIGINT while the command was ending"})
}

func TestCommandWithNoKindFails(t *testing.T) {
	exit, record, _ := run(t, Commands{"stand_in": {Run: func(context.Context, []string, func(error)) (Result, error) {
		return Result{Answer: OK}, nil
	}}}, []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "why": "command stand_in is registered with no Kind"})
}

func TestReportedCrashStopsAServiceAndKeepsItsFacts(t *testing.T) {
	service := Commands{"stand_in": {Kind: Service, Run: func(ctx context.Context, _ []string, crash func(error)) (Result, error) {
		go crash(errors.New("a sink broke"))
		<-ctx.Done()
		return Result{Answer: OK, Facts: Facts{"port": 1}}, nil
	}}}
	exit, record, _ := run(t, service, []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "ended_by": "crash", "why": "before the command finished: a sink broke", "facts": map[string]any{"port": 1}})
}

// A sink that keeps failing reports again from every record its own stop writes; none of those cuts
// the stop short.
func TestCrashWhileACrashedServiceStopsKeepsItsFacts(t *testing.T) {
	service := Commands{"stand_in": {Kind: Service, Run: func(ctx context.Context, _ []string, crash func(error)) (Result, error) {
		crash(errors.New("a sink broke"))
		<-ctx.Done()
		crash(errors.New("a sink broke"))
		return Result{Answer: OK, Facts: Facts{"port": 1}}, nil
	}}}
	exit, record, _ := run(t, service, []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "ended_by": "crash", "why": "before the command finished: a sink broke\nwhile the command was ending: a sink broke", "facts": map[string]any{"port": 1}})
}

func TestReportedCrashEndsATaskAtOnce(t *testing.T) {
	commands := Commands{"stand_in": {Kind: Task, Run: func(_ context.Context, _ []string, crash func(error)) (Result, error) {
		crash(errors.New("a sink broke"))
		select {}
	}}}
	exit, record, _ := run(t, commands, []string{"stand_in"}, nil)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "ended_by": "crash", "why": "before the command finished: a sink broke", "facts": map[string]any{}})
}

func TestSignalWhileACrashedServiceStopsEndsItAtOnce(t *testing.T) {
	signals := make(chan os.Signal, 1)
	service := Commands{"stand_in": {Kind: Service, Run: func(ctx context.Context, _ []string, crash func(error)) (Result, error) {
		crash(errors.New("a sink broke"))
		<-ctx.Done()
		signals <- syscall.SIGTERM
		select {}
	}}}
	exit, record, _ := run(t, service, []string{"stand_in"}, signals)
	if exit != 1 {
		t.Errorf("exit %d, want 1", exit)
	}
	expect(t, record, map[string]any{"outcome": "failed", "ended_by": "crash", "why": "before the command finished: a sink broke\nSIGTERM while the command was ending"})
}

func TestNothingFollowsTheRecord(t *testing.T) {
	var stderr bytes.Buffer
	log := NewLog(&stderr)
	Main(task(func(context.Context, []string) (Result, error) { return Result{Answer: OK}, nil }), []string{"stand_in"}, log, nil)
	if _, err := log.Write([]byte("late\n")); !errors.Is(err, ErrEnded) {
		t.Errorf("write after the record: %v, want ErrEnded", err)
	}
	if strings.Contains(stderr.String(), "late") {
		t.Errorf("a line followed the record: %q", stderr.String())
	}
}
