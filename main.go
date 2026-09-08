// agent-go-debugger — remote debugging CLI for Go targets (Delve embedded).
//
// Mirrors agent-py-debugger / agent-java-debugger: attach to a Go process
// (exec/run under the debugger, or attach to a PID), manage breakpoints,
// step through code, inspect stack/locals/expressions — from a terminal or
// agent scripts via --json events.
package main

import (
	"os"

	"github.com/adamancyzhang/agent-go-debugger/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args, os.Stdin, os.Stdout, os.Stderr))
}
