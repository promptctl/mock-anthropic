package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The binary as a consumer meets it: built, run, and judged by its exit code and its last stderr line.
func TestNoCommand(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mock-anthropic")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("exit: %v, want 2", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout: %q, want nothing", stdout.String())
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	body, found := strings.CutPrefix(last, "USAGE ")
	if !found {
		t.Fatalf("last stderr line: %q, want a USAGE record", last)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(body), &record); err != nil {
		t.Fatal(err)
	}
	if record["outcome"] != "usage" || record["exit"] != float64(2) || record["why"] != "no command given" || record["ended_by"] != nil {
		t.Errorf("record: %v", record)
	}
}
