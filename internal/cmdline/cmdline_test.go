package cmdline

import (
	"errors"
	"flag"
	"slices"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/command"
)

func parse(args ...string) (positionals, trusts []string, err error) {
	var trust List
	positionals, err = Flags("t", func(fs *flag.FlagSet) { fs.Var(&trust, "trust", "") }, args)
	return positionals, trust, err
}

func TestFlagsMayFollowAndPrecedePositionals(t *testing.T) {
	pos, trust, err := parse("--trust", "a", "dir", "--trust", "b", "second")
	if err != nil || !slices.Equal(pos, []string{"dir", "second"}) || !slices.Equal(trust, []string{"a", "b"}) {
		t.Errorf("%v %v %v", pos, trust, err)
	}
}

func TestADoubleDashEndsTheFlags(t *testing.T) {
	pos, trust, err := parse("--trust", "a", "--", "--trust", "x")
	if err != nil || !slices.Equal(pos, []string{"--trust", "x"}) || !slices.Equal(trust, []string{"a"}) {
		t.Errorf("%v %v %v", pos, trust, err)
	}
}

func TestAFlagErrorIsAUsageErrorNamingTheFlagWithTwoDashes(t *testing.T) {
	_, _, err := parse("dir", "--nope")
	var usage *command.UsageError
	if !errors.As(err, &usage) || usage.Why != "flag provided but not defined: --nope" {
		t.Errorf("%v", err)
	}
}

func TestADoubleDashTakenAsAFlagValueDoesNotEndTheFlags(t *testing.T) {
	pos, trust, err := parse("--trust", "--", "dir", "--trust", "x")
	if err != nil || !slices.Equal(pos, []string{"dir"}) || !slices.Equal(trust, []string{"--", "x"}) {
		t.Errorf("%v %v %v", pos, trust, err)
	}
}
