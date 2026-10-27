// Package env is the `env` command (PROJECT.md "Commands"): the environment variables that point a
// Claude Code instance at a running mock, one NAME=value per line, as a shell reads them.
package env

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/cmdline"
	"github.com/promptctl/mock-anthropic/internal/command"
)

// Variable is one environment variable to set.
type Variable struct{ Name, Value string }

// Variables is what an instance sets to reach the mock: the proxy variables in both cases (Claude Code
// and the tools it runs read either), the mock's CA, and the config directory `login` wrote.
func Variables(port int, ca, configDir string) []Variable {
	proxy := "http://127.0.0.1:" + strconv.Itoa(port)
	return []Variable{
		{"HTTPS_PROXY", proxy}, {"HTTP_PROXY", proxy},
		{"https_proxy", proxy}, {"http_proxy", proxy},
		{"NODE_EXTRA_CA_CERTS", ca},
		{"CLAUDE_CONFIG_DIR", configDir},
	}
}

var portText = regexp.MustCompile(`^\d{1,5}$`)

var plain = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Render is v as one line a shell reads back to the same value: a value is single-quoted unless it is
// made only of characters no shell treats specially. A newline has no one-line rendering.
func Render(v Variable) (string, error) {
	if strings.ContainsRune(v.Value, '\n') {
		return "", &command.UsageError{Why: fmt.Sprintf("%s: a path holding a newline cannot be one line", v.Name)}
	}
	if plain.MatchString(v.Value) {
		return v.Name + "=" + v.Value, nil
	}
	return v.Name + "='" + strings.ReplaceAll(v.Value, "'", `'\''`) + "'", nil
}

// Command is env, printing on stdout.
func Command(stdout io.Writer) command.Command {
	return command.Command{
		Kind:  command.Task,
		Usage: "--port N --ca FILE --config-dir DIR",
		Run: func(_ context.Context, args []string, _ func(error), learn func(command.Facts)) (command.Result, error) {
			port, ca, configDir := &cmdline.Once{}, &cmdline.Once{}, &cmdline.Once{}
			rest, err := cmdline.Flags("env", func(fs *flag.FlagSet) {
				fs.Var(port, "port", "")
				fs.Var(ca, "ca", "")
				fs.Var(configDir, "config-dir", "")
			}, args)
			if err != nil {
				return command.Result{}, err
			}
			if len(rest) > 0 {
				return command.Result{}, &command.UsageError{Why: "unexpected argument: " + rest[0]}
			}
			for name, f := range map[string]*cmdline.Once{"--port": port, "--ca": ca, "--config-dir": configDir} {
				if f.Value == nil {
					return command.Result{}, &command.UsageError{Why: "missing " + name}
				}
			}
			n, err := strconv.Atoi(*port.Value)
			if !portText.MatchString(*port.Value) || err != nil || n < 1 || n > 65535 {
				return command.Result{}, &command.UsageError{Why: fmt.Sprintf("--port %q: not a port number from 1 to 65535", *port.Value)}
			}
			caPath, err := cmdline.File("--ca", *ca.Value)
			if err != nil {
				return command.Result{}, err
			}
			dir, err := cmdline.Absolute("--config-dir", *configDir.Value)
			if err != nil {
				return command.Result{}, err
			}
			vars := Variables(n, caPath, dir)
			var out strings.Builder
			names := []string{}
			for _, v := range vars {
				line, err := Render(v)
				if err != nil {
					return command.Result{}, err
				}
				out.WriteString(line + "\n")
				names = append(names, v.Name)
			}
			// [LAW:nothing-unseen] The record names what was printed, not only that something was.
			facts := command.Facts{"port": n, "ca": caPath, "config_dir": dir, "variables": names}
			learn(facts)
			if _, err := io.WriteString(stdout, out.String()); err != nil {
				return command.Result{}, fmt.Errorf("writing the variables: %w", err)
			}
			return command.Result{Answer: command.OK, Facts: facts}, nil
		},
	}
}
