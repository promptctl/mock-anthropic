package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
)

var (
	buildOnce sync.Once
	built     string
	buildErr  error
)

// binary is the mock as a consumer meets it, built once per test run into a directory of its own, so
// the certificates it generates beside itself are this run's.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mock-anthropic-test")
		if err != nil {
			buildErr = err
			return
		}
		built = filepath.Join(dir, "mock-anthropic")
		if out, err := exec.Command("go", "build", "-o", built, ".").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return built
}

func TestMain(m *testing.M) {
	code := m.Run()
	if built != "" {
		os.RemoveAll(filepath.Dir(built))
	}
	os.Exit(code)
}

// finished is a run judged as a consumer judges it: its exit code, its stdout, every stderr line, and
// the record its last stderr line carries, whose capitals must be its outcome.
type finished struct {
	exit   int
	stdout string
	stderr []string
	record map[string]any
}

func finish(t *testing.T, cmd *exec.Cmd, stdout string, stderr *bytes.Buffer, err error) finished {
	t.Helper()
	exit := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	head, body, _ := strings.Cut(last, " ")
	var record map[string]any
	if err := json.Unmarshal([]byte(body), &record); err != nil {
		t.Fatalf("last stderr line is not a record: %q: %v", last, err)
	}
	if outcome, _ := record["outcome"].(string); head != strings.ToUpper(outcome) {
		t.Fatalf("line opens with %q, record outcome is %q", head, outcome)
	}
	return finished{exit, stdout, lines, record}
}

func run(t *testing.T, args ...string) finished {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary(t), args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return finish(t, cmd, stdout.String(), &stderr, cmd.Run())
}

// running is a started serve: the ready line it printed, parsed, and the process.
type running struct {
	cmd    *exec.Cmd
	line   string
	ready  map[string]any
	rest   chan string
	stderr *bytes.Buffer
}

func startServe(t *testing.T, args ...string) *running {
	t.Helper()
	cmd := exec.Command(binary(t), append([]string{"serve"}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	r := &running{cmd: cmd, rest: make(chan string, 1), stderr: &bytes.Buffer{}}
	cmd.Stderr = r.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	reader := bufio.NewReader(stdout)
	r.line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("no ready line: %v; stderr: %s", err, r.stderr)
	}
	if err := json.Unmarshal([]byte(r.line), &r.ready); err != nil {
		t.Fatalf("ready line %q: %v", r.line, err)
	}
	go func() {
		more, _ := io.ReadAll(reader)
		r.rest <- string(more)
	}()
	return r
}

func (r *running) port() int { return int(r.ready["port"].(float64)) }

// stop delivers sig to the process and waits for it to end.
func (r *running) stop(t *testing.T, sig syscall.Signal) finished {
	t.Helper()
	if err := r.cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	err := r.cmd.Wait()
	return finish(t, r.cmd, r.line+<-r.rest, r.stderr, err)
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

func TestNoCommand(t *testing.T) {
	result := run(t)
	if result.exit != 2 || result.stdout != "" {
		t.Errorf("exit %d, stdout %q; want 2 and nothing", result.exit, result.stdout)
	}
	expect(t, result.record, map[string]any{"outcome": "usage", "exit": 2, "why": "no command given", "ended_by": nil})
}

func TestServeRecordsTheRunWhenStopped(t *testing.T) {
	mock := startServe(t, "--port", "0")
	keys := make([]string, 0, len(mock.ready))
	for k := range mock.ready {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"ca", "port"}) || mock.port() <= 0 {
		t.Fatalf("ready line %q", mock.line)
	}
	ca := mock.ready["ca"].(string)
	if _, err := os.Stat(ca); err != nil {
		t.Errorf("the CA it names: %v", err)
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/nowhere", mock.port()))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	result := mock.stop(t, syscall.SIGTERM)
	if result.exit != 0 || result.stdout != mock.line {
		t.Errorf("exit %d, stdout %q; want 0 and only the ready line", result.exit, result.stdout)
	}
	if !slices.ContainsFunc(result.stderr, func(l string) bool { return strings.HasPrefix(l, "UNMATCHED ") && strings.Contains(l, `"/v1/nowhere"`) }) {
		t.Errorf("no UNMATCHED line for the request: %q", result.stderr)
	}
	expect(t, result.record, map[string]any{
		"event": "command", "argv": []string{"serve", "--port", "0"}, "outcome": "ok", "exit": 0, "ended_by": "SIGTERM",
	})
	facts := result.record["facts"].(map[string]any)
	expect(t, facts, map[string]any{"port": mock.port(), "ca": ca, "account_file": nil, "scenarios": []string{}})
	certificates := facts["certificates"].(map[string]any)
	if len(certificates) != 2 {
		t.Errorf("certificates %v, want ca and leaf", certificates)
	}
	for _, which := range []string{"ca", "leaf"} {
		if !slices.Contains([]any{"generated", "reused", "lost-race"}, certificates[which]) {
			t.Errorf("certificates.%s = %v", which, certificates[which])
		}
	}
}

func TestServeRecordsItsInputsAbsoluteAndInOrder(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, d := range []string{first, second} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	accountFile := filepath.Join(dir, "account.json")
	if err := os.WriteFile(accountFile, []byte(`{"accountUuid":"00000000-0000-4000-8000-000000000065","emailAddress":"override@mock-anthropic.invalid","organizationUuid":"00000000-0000-4000-8000-000000000002","organizationName":"Mock Org","subscriptionType":"max"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdDir, _ := os.Getwd()
	relative, err := filepath.Rel(cmdDir, second)
	if err != nil {
		t.Fatal(err)
	}
	mock := startServe(t, "--account", accountFile, "--scenarios", first, "--scenarios", relative)
	result := mock.stop(t, syscall.SIGINT)
	if result.exit != 0 {
		t.Fatalf("exit %d: %q", result.exit, result.stderr)
	}
	expect(t, result.record["facts"].(map[string]any), map[string]any{"account_file": accountFile, "scenarios": []string{first, second}})
}

// Stop waits for every unit in flight to write its record. Here nobody reads stderr until both signals
// are sent, so once the pipe is full a record cannot be written, and the stop cannot finish before the
// second signal arrives.
func TestServeSecondSignalWhileStoppingFails(t *testing.T) {
	cmd := exec.Command(binary(t), "serve", "--port", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var ready struct{ Port int }
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		t.Fatal(err)
	}
	// Each UNMATCHED line carries its path: 40 of them are more than any pipe buffer holds.
	path := "/" + strings.Repeat("x", 8000)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	var requests sync.WaitGroup
	for range 40 {
		requests.Go(func() {
			if response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", ready.Port, path)); err == nil {
				response.Body.Close()
			}
		})
	}
	requests.Wait()
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
	}
	var lines bytes.Buffer
	if _, err := io.Copy(&lines, stderr); err != nil {
		t.Fatal(err)
	}
	result := finish(t, cmd, line, &lines, cmd.Wait())
	if result.exit != 1 {
		t.Errorf("exit %d, want 1", result.exit)
	}
	expect(t, result.record, map[string]any{"outcome": "failed", "facts": map[string]any{}})
	// Both signals are pending before the dispatcher reads either; which it reads first is the runtime's.
	first, _ := result.record["ended_by"].(string)
	second := map[string]string{"SIGTERM": "SIGINT", "SIGINT": "SIGTERM"}[first]
	if want := first + " before the command finished\n" + second + " while the command was ending"; second == "" || result.record["why"] != want {
		t.Errorf("ended_by %q, why %q; want one of SIGTERM and SIGINT ending the other's stop", first, result.record["why"])
	}
}

func TestServeOnAPortInUseFails(t *testing.T) {
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	result := run(t, "serve", "--port", fmt.Sprint(holder.Addr().(*net.TCPAddr).Port))
	if result.exit != 1 || result.stdout != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing", result.exit, result.stdout)
	}
	expect(t, result.record, map[string]any{"outcome": "failed", "exit": 1, "ended_by": nil, "facts": map[string]any{}})
}

func TestServeWrongCommandLine(t *testing.T) {
	notAnAccount := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(notAnAccount, []byte(`{"emailAddress":"not an email"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		what  string
		args  []string
		named string
	}{
		{"an unknown flag", []string{"--nope"}, "--nope"},
		{"a positional", []string{"extra"}, "unexpected argument: extra"},
		{"a port that is not a number", []string{"--port", "abc"}, `"abc"`},
		{"a port out of range", []string{"--port", "70000"}, `"70000"`},
		{"a missing account file", []string{"--account", "/nonexistent/account.json"}, "/nonexistent/account.json"},
		{"an account file that is not an account", []string{"--account", notAnAccount}, notAnAccount},
		{"a missing scripts directory", []string{"--scenarios", "/nonexistent/scripts"}, "/nonexistent/scripts"},
		{"an empty scripts directory path", []string{"--scenarios", ""}, "--scenarios: an empty path"},
		{"an empty account file path", []string{"--account", ""}, "--account: an empty path"},
		{"a flag that takes one value, given twice", []string{"--port", "0", "--port", "1"}, "--port: given more than once"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			argv := append([]string{"serve"}, tc.args...)
			result := run(t, argv...)
			if result.exit != 2 || result.stdout != "" {
				t.Errorf("exit %d, stdout %q; want 2 and nothing", result.exit, result.stdout)
			}
			expect(t, result.record, map[string]any{"outcome": "usage", "argv": argv, "ended_by": nil, "facts": map[string]any{}})
			if why, _ := result.record["why"].(string); !strings.Contains(why, tc.named) {
				t.Errorf("why %q does not name %q", why, tc.named)
			}
		})
	}
}
