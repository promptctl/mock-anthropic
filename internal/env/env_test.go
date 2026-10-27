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

func cfgDir(t *testing.T, name string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
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
	dir := cfgDir(t, "my config \"dir\"")
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
	ca, dir := caFile(t), cfgDir(t, "cfg")
	out, _, err := run(t, "--port", "1", "--ca", ca, "--config-dir", dir)
	if err != nil || !strings.Contains(out, "\nCLAUDE_CONFIG_DIR="+dir+"\n") || !strings.HasPrefix(out, "HTTPS_PROXY=http://127.0.0.1:1\n") {
		t.Errorf("%q %v", out, err)
	}
}

func TestAWrongCommandLineIsAUsageError(t *testing.T) {
	ca, notFile, cfg := caFile(t), t.TempDir(), cfgDir(t, "c")
	for name, args := range map[string][]string{
		"nothing":           {},
		"no port":           {"--ca", ca, "--config-dir", cfg},
		"no ca":             {"--port", "1", "--config-dir", cfg},
		"no config dir":     {"--port", "1", "--ca", ca},
		"port 0":            {"--port", "0", "--ca", ca, "--config-dir", cfg},
		"port too large":    {"--port", "65536", "--ca", ca, "--config-dir", cfg},
		"port text":         {"--port", "http", "--ca", ca, "--config-dir", cfg},
		"a missing ca":      {"--port", "1", "--ca", ca + ".nope", "--config-dir", cfg},
		"a directory as ca": {"--port", "1", "--ca", notFile, "--config-dir", cfg},
		"an empty config":   {"--port", "1", "--ca", ca, "--config-dir", ""},
		"a newline in path": {"--port", "1", "--ca", ca, "--config-dir", cfg + "\nd"},
		"a missing config":  {"--port", "1", "--ca", ca, "--config-dir", cfg + ".nope"},
		"a file as config":  {"--port", "1", "--ca", ca, "--config-dir", ca},
		"port twice":        {"--port", "1", "--port", "2", "--ca", ca, "--config-dir", cfg},
		"a stray argument":  {"--port", "1", "--ca", ca, "--config-dir", cfg, "extra"},
	} {
		out, _, err := run(t, args...)
		var usage *command.UsageError
		if !errors.As(err, &usage) || out != "" {
			t.Errorf("%s: %q, %v, want a UsageError and no output", name, out, err)
		}
	}
}

func TestTheRecordNamesWhatWasPrinted(t *testing.T) {
	ca, cfg := caFile(t), cfgDir(t, "c")
	_, res, err := run(t, "--port", "7", "--ca", ca, "--config-dir", cfg)
	names, _ := res.Facts["variables"].([]string)
	if err != nil || res.Facts["port"] != 7 || res.Facts["ca"] != ca || res.Facts["config_dir"] != cfg ||
		strings.Join(names, " ") != "HTTPS_PROXY HTTP_PROXY https_proxy http_proxy NODE_EXTRA_CA_CERTS CLAUDE_CONFIG_DIR" {
		t.Errorf("%v %v", res.Facts, err)
	}
}

// login names the keychain entry for the NFC path, so env prints that same string.
func TestTheConfigDirIsPrintedNFC(t *testing.T) {
	nfd := cfgDir(t, "cafe\u0301")
	out, res, err := run(t, "--port", "1", "--ca", caFile(t), "--config-dir", nfd)
	nfc := filepath.Join(filepath.Dir(nfd), "caf\u00e9")
	if err != nil || res.Facts["config_dir"] != nfc || !strings.Contains(out, "CLAUDE_CONFIG_DIR='"+nfc+"'\n") {
		t.Errorf("%q %v %v", out, res.Facts, err)
	}
}

func TestACarriageReturnHasNoOneLineRendering(t *testing.T) {
	if _, err := Render(Variable{"X", "a\rb"}); err == nil {
		t.Error("a carriage return was rendered")
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("EPIPE") }

// The record names what was printed: a write that failed printed nothing.
func TestAFailedWriteIsNotRecordedAsPrinted(t *testing.T) {
	learned := false
	_, err := Command(failing{}).Run(context.Background(), []string{"--port", "1", "--ca", caFile(t), "--config-dir", cfgDir(t, "c")},
		func(error) {}, func(command.Facts) { learned = true })
	if err == nil || learned {
		t.Errorf("%v, learned %v", err, learned)
	}
}

// The usage error names the first missing flag in the order Usage lists them.
func TestTheFirstMissingFlagIsNamed(t *testing.T) {
	_, _, err := run(t)
	if err == nil || !strings.Contains(err.Error(), "missing --port") {
		t.Errorf("%v", err)
	}
}
