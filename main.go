// Command mock-anthropic is the mock: `mock-anthropic <command> [args]` (PROJECT.md "Commands").
package main

import (
	"os"

	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/coverage"
	"github.com/promptctl/mock-anthropic/internal/serve"
)

// stderr is the invocation's one stderr, shared by the dispatcher and every command's records.
var stderr = command.NewLog(os.Stderr)

// Commands is every command the mock answers to; each later command registers here.
var Commands = command.Commands{
	"serve":    serve.Command(os.Stdout, stderr),
	"coverage": coverage.Command(os.Stdout),
}

func main() {
	os.Exit(command.Main(Commands, os.Args[1:], stderr, command.Notify()))
}
