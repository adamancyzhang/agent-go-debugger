// Package cli parses top-level subcommands (attach, version, help) and
// drives a console.Runner session. Exit codes: 0 ok, 1 command failures,
// 2 usage/connection errors, 130 interrupted by signal.
package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/adamancyzhang/agent-go-debugger/internal/console"
	"github.com/adamancyzhang/agent-go-debugger/internal/delvez"
)

// Version of the CLI.
const Version = "0.1.1"

// attachFlags holds the session flags of `attach`.
type attachFlags struct {
	host        string
	port        int
	json        bool
	execCmds    []string
	hold        bool
	inPath      string
	cmdTimeout  time.Duration
	resumeAfter time.Duration
}

func defaultAttachFlags() attachFlags {
	return attachFlags{
		host:        "127.0.0.1",
		resumeAfter: console.DefaultResumeAfter,
	}
}

// Main is the CLI entry point; returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		usage(stdout)
		return 2
	}
	cmd, rest := args[1], args[2:]
	switch cmd {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "agent-go-debugger %s\n", Version)
		return 0
	case "attach":
		return runAttach(rest, stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "agent-go-debugger: error: unknown command %q\n", cmd)
		usage(stdout)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `agent-go-debugger — remote debugging CLI for Go targets

Connects over TCP to a Delve headless server running next to the target
(dlv exec|attach --headless --listen=:PORT --api-version=2), and drives it:
breakpoints, stepping, stack/locals/eval, goroutines. The CLI never manages
the target process — only the debug port.

usage:
  agent-go-debugger attach [flags] --port PORT
  agent-go-debugger version | help

flags:
  --host HOST        dlv headless host (default 127.0.0.1)
  --port PORT        dlv headless JSON-RPC port (required)
  --json             emit JSONL events (one per line) on stdout
  --exec "CMD"       run session commands then detach (repeatable)
  --hold             keep the session alive after --exec (persistent debug)
  --in PATH          read further commands from PATH (fifo/file) when holding
  --timeout S        per blocking command wait (0 = forever)
  --resume-after S   stop-hold guard: auto-resume target after S (0 = off)

session commands: bp add|list|remove|enable|disable|clear|condition,
  continue|next|step|fin, halt, stack, locals, args, eval EXPR,
  goroutines, goroutine <id>, source, status, quit, help
`)
}

func runAttach(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	af := defaultAttachFlags()
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			af.json = true
		case a == "--hold":
			af.hold = true
		case a == "--host":
			i++
			if i < len(args) {
				af.host = args[i]
			}
		case a == "--port":
			i++
			if i < len(args) {
				p, err := strconv.Atoi(args[i])
				if err != nil || p <= 0 {
					fmt.Fprintf(stderr, "agent-go-debugger: error: bad --port %q\n", args[i])
					return 2
				}
				af.port = p
			}
		case a == "--in":
			i++
			if i < len(args) {
				af.inPath = args[i]
			}
		case a == "--exec":
			i++
			if i < len(args) {
				af.execCmds = append(af.execCmds, args[i])
			}
		case a == "--timeout":
			i++
			if i < len(args) {
				d, err := parseSeconds(args[i])
				if err != nil {
					fmt.Fprintf(stderr, "agent-go-debugger: error: bad --timeout\n")
					return 2
				}
				af.cmdTimeout = d
			}
		case a == "--resume-after":
			i++
			if i < len(args) {
				d, err := parseSeconds(args[i])
				if err != nil {
					fmt.Fprintf(stderr, "agent-go-debugger: error: bad --resume-after\n")
					return 2
				}
				af.resumeAfter = d
			}
		default:
			fmt.Fprintf(stderr, "agent-go-debugger: error: unknown attach argument %s\n", a)
			return 2
		}
	}
	if af.port == 0 {
		fmt.Fprintf(stderr, "agent-go-debugger: error: attach requires --port PORT\n")
		return 2
	}
	sess, err := delvez.Connect(af.host, af.port)
	if err != nil {
		fmt.Fprintf(stderr, "agent-go-debugger: error: %v\n", err)
		return 2
	}
	return runSession(sess, af, stdin, stdout, stderr)
}

func parseSeconds(s string) (time.Duration, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, fmt.Errorf("negative")
	}
	return time.Duration(f * float64(time.Second)), nil
}

func runSession(sess *delvez.Session, af attachFlags, stdin io.Reader, stdout, stderr io.Writer) int {
	in := stdin
	if af.inPath != "" {
		f, err := os.Open(af.inPath)
		if err != nil {
			fmt.Fprintf(stderr, "agent-go-debugger: error: open --in %s: %v\n", af.inPath, err)
			sess.Detach(false)
			return 2
		}
		defer f.Close()
		in = f
	}
	o := console.Options{
		JSON:        af.json,
		CmdTimeout:  af.cmdTimeout,
		ResumeAfter: af.resumeAfter,
	}
	r := console.New(sess, o, stdout, stderr)

	// SIGINT: interrupt the session and disconnect the target safely (the
	// target keeps running; breakpoints stay on the server).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		r.Abort()
		sess.Quit()
	}()

	code := r.RunScript(af.execCmds, af.hold, in)
	signal.Stop(sig)
	return code
}
