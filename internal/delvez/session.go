// Package delvez is the only package that imports Delve directly.
//
// It wraps Delve's service.Client (JSON-RPC client for a remote
// `dlv --headless --listen=:port --api-version=2` server) into a small
// session model whose semantics mirror agent-py-debugger /
// agent-java-debugger:
//
//   - the CLI never manages the target process: it connects to the debug
//     port the target side exposes (like JDWP for Java) and drives it;
//   - the target runs by default after attach (auto-continue): only a
//     breakpoint hit (or an explicit execution command) ever holds it;
//   - commands that need a stopped target (breakpoint management, stepping)
//     transparently halt + resume around their critical section (Interrupt);
//   - all stops flow through a generation-tagged channel (see control.go).
package delvez

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-delve/delve/service"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/rpc2"
)

// Session drives a remote Delve headless server over its JSON-RPC port.
type Session struct {
	cli  service.Client
	addr string // host:port of the dlv headless server

	mode string // "remote"

	ctrl *control

	lifeMu    sync.Mutex
	exited    bool
	exitStats int
	probeCh   chan struct{}
	probeOnce sync.Once
}

func (s *Session) signalExited() { s.probeOnce.Do(func() { close(s.probeCh) }) }

// Probe returns a channel closed when the target is observed to have exited.
func (s *Session) Probe() <-chan struct{} { return s.probeCh }

func (s *Session) noteExit(status int) {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.exited = true
	s.exitStats = status
	s.signalExited()
}

// Connect dials a `dlv --headless --listen=HOST:PORT --api-version=2`
// server. The target keeps running (a fresh dlv server waits at the entry
// point until its first continue — we issue it unless it is already
// running).
func Connect(host string, port int) (*Session, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	cli := rpc2.NewClient(addr)
	// Verify the connection: version round-trip.
	if v := cli.GetVersion(); v == nil || v.DelveVersion == "" {
		cli.Detach(false)
		return nil, fmt.Errorf("cannot reach a Delve headless server at %s (start it with: dlv exec|attach --headless --listen=%s --api-version=2 --accept-multiclient -- <target>)", addr, addr)
	}
	s := &Session{cli: cli, addr: addr, mode: "remote", probeCh: make(chan struct{})}
	s.ctrl = newControl(s)
	// Default runnable state: if the target sits at its entry point (a dlv
	// server started without --continue), let it serve. A target stopped at
	// a breakpoint/step is left exactly where it is — inspection commands
	// then see the real stop context, and execution resumes only on an
	// explicit continue.
	st, err := s.State(true)
	if err == nil && st != nil && !st.Running && !st.Exited && st.StopReason == "" {
		s.ctrl.startRun()
	}
	return s, nil
}

// isProcessExited reports whether err says the target process exited.
// The RPC layer returns plain errors, so we match on the shape of the text.
func isProcessExited(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, frag := range []string{"process exited", "has exited", "no such process"} {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

// --- lifecycle -------------------------------------------------------------

// Mode reports the connection mode ("remote").
func (s *Session) Mode() string { return s.mode }

// Pid reports the debugged process pid.
//
// NOTE: ProcessPid is an RPC that the headless server only answers while the
// target is stopped; calling it against a running target blocks forever, so
// we never query it live. Remote sessions report 0 unless the pid was
// captured while stopped.
func (s *Session) Pid() int {
	st, err := s.cli.GetStateNonBlocking()
	if err != nil || st == nil || st.Running || st.Exited {
		return 0
	}
	// State carries the pid in the RPC reply; use it if present.
	return st.Pid
}

// Addr reports host:port of the headless server.
func (s *Session) Addr() string { return s.addr }

// ExePath: unknown over the wire; empty for remote sessions.
func (s *Session) ExePath() string { return "" }

// Workdir: unknown over the wire; empty for remote sessions.
func (s *Session) Workdir() string { return "" }

// Running reports whether the target is currently executing.
func (s *Session) Running() bool {
	st, err := s.State(true)
	return err == nil && st != nil && st.Running
}

// Exited reports whether the target process has exited and with what status.
func (s *Session) Exited() (bool, int) {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.exited, s.exitStats
}

// State asks the server for the current state (non-blocking form).
func (s *Session) State(nowait bool) (*api.DebuggerState, error) {
	if nowait {
		return s.cli.GetStateNonBlocking()
	}
	return s.cli.GetState()
}

// Quit ends this client session cleanly:
//   - if the target is stopped (e.g. on one of our stops), it is resumed
//     first and we wait until the server confirms it is running, so a
//     stopped service is never left frozen;
//   - the headless server and its breakpoints stay alive for the next
//     client (Detach would tear down the whole server and kill the target).
//
// Delve's own Disconnect(cont=true) races: it fires a continue and closes
// the connection immediately, and the server interrupts the in-flight
// continue when it sees the disconnect — hence the explicit resume + poll.
func (s *Session) Quit() error {
	if st, err := s.State(true); err == nil && st != nil && !st.Running && !st.Exited {
		// Target stopped: resume it and wait until the server confirms
		// execution before closing the connection.
		go s.ctrl.startRun()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			st, err := s.State(true)
			if err != nil {
				break
			}
			if st != nil && (st.Running || st.Exited) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if err := s.cli.Disconnect(false); err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}
	return nil
}

// Disconnect ends the client session leaving the target exactly as it is.
// Prefer Quit() — Disconnect is for tooling that manages its own resume.
func (s *Session) Disconnect(cont bool) error {
	if err := s.cli.Disconnect(cont); err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}
	return nil
}

// Detach releases the debugger from the target entirely. killProcess=false
// leaves the target running (but a lone detach also ends the headless server
// session), killProcess=true terminates it first. Use Detach(true) only to
// kill an exec-mode target; prefer Disconnect for session teardown.
func (s *Session) Detach(killProcess bool) error {
	if killProcess && s.Running() {
		if _, err := s.ctrl.halt(); err != nil && !isProcessExited(err) {
			return fmt.Errorf("halt before detach: %w", err)
		}
	}
	if err := s.cli.Detach(killProcess); err != nil {
		return fmt.Errorf("detach: %w", err)
	}
	if killProcess {
		s.noteExit(0)
	}
	return nil
}

// Kill terminates the target via the server (exec-mode targets only; the
// server owns the process).
func (s *Session) Kill() error {
	return s.Detach(true)
}

// CancelWait interrupts any blocking RunToStop (used on SIGINT). Subsequent
// waits return immediately; the session is shutting down.
func (s *Session) CancelWait() { s.ctrl.cancelWait() }

// --- execution control (control.go) ----------------------------------------

// RunToStop runs a blocking execution command (continue/next/step/stepOut)
// and waits up to timeout for the stop it produces. See control.go.
func (s *Session) RunToStop(name string, timeout time.Duration) *StopInfo {
	return s.ctrl.RunToStop(name, timeout)
}

// PollStop returns a pending stop without blocking, or ok=false.
func (s *Session) PollStop() (*StopInfo, bool) { return s.ctrl.PollStop() }

// Interrupt stops the target (if running), runs fn while it is stopped, and
// resumes it if it was running. All commands that need a quiesced target
// (breakpoint management, inspection) run inside Interrupt.
func (s *Session) Interrupt(fn func() error) error { return s.ctrl.Interrupt(fn) }

// HaltNow stops a running target without issuing a follow-up command.
func (s *Session) HaltNow() error {
	if _, err := s.ctrl.halt(); err != nil {
		if isProcessExited(err) {
			return errors.New("target has exited")
		}
		return err
	}
	return nil
}

// SwitchGoroutine makes id the current goroutine for subsequent inspection.
func (s *Session) SwitchGoroutine(id int64) error {
	return s.Interrupt(func() error {
		_, err := s.cli.SwitchGoroutine(id)
		return err
	})
}

// TraceHit is one buffered tracepoint record (a tracepoint logs hits without
// pausing the target).
type TraceHit struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Address  uint64 `json:"addr"`
}

// traceFlusher is implemented by the RPC client (not by the general Client
// interface).
type traceFlusher interface {
	GetBufferedTracepoints(*api.LoadConfig) ([]api.TracepointResult, error)
}

// FlushTraces drains the server's buffered tracepoint records since the last
// execution command. Runs inside Interrupt (the RPC needs a stopped target).
func (s *Session) FlushTraces() []TraceHit {
	tf, ok := s.cli.(traceFlusher)
	if !ok {
		return nil
	}
	var out []TraceHit
	cfg := &api.LoadConfig{FollowPointers: true, MaxVariableRecurse: 1, MaxStringLen: 64, MaxArrayValues: 16, MaxStructFields: 8}
	_ = s.Interrupt(func() error {
		results, err := tf.GetBufferedTracepoints(cfg)
		if err != nil {
			return err
		}
		for _, r := range results {
			out = append(out, TraceHit{File: r.File, Line: r.Line, Function: r.FunctionName, Address: r.Addr})
		}
		return nil
	})
	return out
}
