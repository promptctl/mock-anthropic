// Command mock-anthropic is the mock: `mock-anthropic <command> [args]` (PROJECT.md "Commands").
package main

import (
	"os"

	"github.com/promptctl/mock-anthropic/internal/command"
)

// Commands is every command the mock answers to; each later command registers here.
var Commands = command.Commands{}

func main() {
	os.Exit(command.Main(Commands, os.Args[1:], os.Stderr, command.Notify()))
}
