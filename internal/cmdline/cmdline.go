// Package cmdline is what every command's command line is parsed with, so a flag given twice, a path
// left empty, or a flag the command does not know is a wrong command line in the same words everywhere.
package cmdline

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/command"
)

// oneDash is the flag name in each error package flag writes, which it gives one dash; anchored to the
// message, so a dash inside a flag's value stays as given.
var oneDash = regexp.MustCompile(`^(flag provided but not defined: |flag needs an argument: |invalid value ".*" for flag )-`)

// Once is a flag that takes one value; given twice, it is a wrong command line. Value is nil until given.
type Once struct {
	Value *string
}

func (o *Once) String() string { return "" }
func (o *Once) Set(v string) error {
	if o.Value != nil {
		return errors.New("given more than once")
	}
	o.Value = &v
	return nil
}

// List is a flag that may be given any number of times, each value kept in order.
type List []string

func (l *List) String() string     { return "" }
func (l *List) Set(v string) error { *l = append(*l, v); return nil }

// Flags parses args against the flags declare puts on a flag set, and returns the positional arguments
// in order. Flags may follow a positional, as the usage lines write them (`login <config-dir> --trust DIR`);
// package flag alone stops at the first positional. A bare `--` ends the flags. Every failure is a
// UsageError; flag names a flag by one dash, while PROJECT.md and the usage lines name every flag by two.
func Flags(name string, declare func(*flag.FlagSet), args []string) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	declare(fs)
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, &command.UsageError{Why: oneDash.ReplaceAllString(err.Error(), "${1}--")}
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positionals, nil
		}
		if consumed := len(args) - len(rest); consumed > 0 && endedFlags(fs, args[:consumed]) {
			return append(positionals, rest...), nil
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
}

// endedFlags reports whether a parse that consumed these args ended on a bare `--`. The last token alone
// cannot say: `--trust --` consumes `--` as the flag's value. Replaying the tokens tells a value from a
// terminator, as package flag itself reads them.
func endedFlags(fs *flag.FlagSet, consumed []string) bool {
	for i := 0; i < len(consumed); i++ {
		tok := consumed[i]
		if tok == "--" {
			return i == len(consumed)-1
		}
		name := strings.TrimLeft(tok, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
				i++ // the next token is this flag's value
			}
		}
	}
	return false
}

// Absolute is a command-line path made absolute, so a record names it whatever the reader's working
// directory. An empty one (an unset shell variable) is a wrong command line, not the working directory.
func Absolute(given, text string) (string, error) {
	if text == "" {
		return "", &command.UsageError{Why: given + ": an empty path"}
	}
	path, err := filepath.Abs(text)
	if err != nil {
		return "", &command.UsageError{Why: fmt.Sprintf("%s %s: %v", given, text, err)}
	}
	return path, nil
}

// Directory is a command-line path that must name a directory there now.
func Directory(given, text string) (string, error) {
	path, err := Absolute(given, text)
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
