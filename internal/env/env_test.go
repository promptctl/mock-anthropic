package env

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/command"
)

func run(t *testing.T, args ...string) (string, command.Result, error) {
	t.Helper()
	var out bytes.Buffer
	res, err := Command(&out).Run(context.Background(), args, func(error) {}, func(command.Facts) {})
	return out.String(), res, err
}

func caFile(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The output is read by a shell, so the test reads it with one: every value comes back byte-equal,
// whatever the paths hold.
func TestTheOutputIsReadBackByAShellToTheSameValues(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "a dir with 'quotes' and $x `y` ;&", "ca.pem")
	os.MkdirAll(filepath.Dir(ca), 0o700)
	os.WriteFile(ca, []byte("x"), 0o600)
	dir := filepath.Join(t.TempDir(), "my config \"dir\"")
	out, res, err := run(t, "--port", "4040", "--ca", ca, "--config-dir", dir)
	if err != nil || res.Answer != command.OK {
		t.Fatalf("%v %v", res, err)
	}
	want := map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:4040", "HTTP_PROXY": "http://127.0.0.1:4040",
		"https_proxy": "http://127.0.0.1:4040", "http_proxy": "http://127.0.0.1:4040",
		"NODE_EXTRA_CA_CERTS": ca, "CLAUDE_CONFIG_DIR": dir,
	}
	for name, value := range want {
		cmd := exec.Command("sh", "-c", `set -a; eval "$LINES"; printf %s "$`+name+`"`)
		cmd.Env = []string{"LINES=" + out}
		got, err := cmd.Output()
		if err != nil || string(got) != value {
			t.Errorf("%s: %q, %v, want %q", name, got, err, value)
		}
	}
	if n := strings.Count(out, "\n"); n != len(want) {
		t.Errorf("%d lines, want %d: %q", n, len(want), out)
	}
}

func TestASafeValueIsPrintedBare(t *testing.T) {
	ca := caFile(t)
	out, _, err := run(t, "--port", "1", "--ca", ca, "--config-dir", "/tmp/cfg")
	if err != nil || !strings.Contains(out, "\nCLAUDE_CONFIG_DIR=/tmp/cfg\n") || !strings.HasPrefix(out, "HTTPS_PROXY=http://127.0.0.1:1\n") {
		t.Errorf("%q %v", out, err)
	}
}

func TestAWrongCommandLineIsAUsageError(t *testing.T) {
	ca, notFile := caFile(t), t.TempDir()
	for name, args := range map[string][]string{
		"nothing":           {},
		"no port":           {"--ca", ca, "--config-dir", "/c"},
		"no ca":             {"--port", "1", "--config-dir", "/c"},
		"no config dir":     {"--port", "1", "--ca", ca},
		"port 0":            {"--port", "0", "--ca", ca, "--config-dir", "/c"},
		"port too large":    {"--port", "65536", "--ca", ca, "--config-dir", "/c"},
		"port text":         {"--port", "http", "--ca", ca, "--config-dir", "/c"},
		"a missing ca":      {"--port", "1", "--ca", ca + ".nope", "--config-dir", "/c"},
		"a directory as ca": {"--port", "1", "--ca", notFile, "--config-dir", "/c"},
		"an empty config":   {"--port", "1", "--ca", ca, "--config-dir", ""},
		"a newline in path": {"--port", "1", "--ca", ca, "--config-dir", "/c\nd"},
		"port twice":        {"--port", "1", "--port", "2", "--ca", ca, "--config-dir", "/c"},
		"a stray argument":  {"--port", "1", "--ca", ca, "--config-dir", "/c", "extra"},
	} {
		out, _, err := run(t, args...)
		var usage *command.UsageError
		if !errors.As(err, &usage) || out != "" {
			t.Errorf("%s: %q, %v, want a UsageError and no output", name, out, err)
		}
	}
}

func TestTheRecordNamesWhatWasPrinted(t *testing.T) {
	ca := caFile(t)
	_, res, err := run(t, "--port", "7", "--ca", ca, "--config-dir", "/c")
	names, _ := res.Facts["variables"].([]string)
	if err != nil || res.Facts["port"] != 7 || res.Facts["ca"] != ca || res.Facts["config_dir"] != "/c" ||
		strings.Join(names, " ") != "HTTPS_PROXY HTTP_PROXY https_proxy http_proxy NODE_EXTRA_CA_CERTS CLAUDE_CONFIG_DIR" {
		t.Errorf("%v %v", res.Facts, err)
	}
}
