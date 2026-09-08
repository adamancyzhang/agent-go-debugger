package delvez

// Execution control over the remote headless server.
//
// Model:
//   - The server's non-blocking state is the source of truth for
//     "target executing".
//   - Execution commands (continue/next/step/stepOut) run on their own
//     goroutine; when they produce a stop (a breakpoint hit, a completed
//     step, an exit) the resulting StopInfo is posted to stopCh wrapped in
//     the generation current when the command was issued.
//   - The generation is raised on every intentional halt. A stop posted
//     under an older generation comes from an execution we interrupted
//     ourselves, so it is dropped — a breakpoint is only ever reported once,
//     by the continue that actually hit it.
//   - The console loop is the single reader: every iteration it polls for
//     stops (so async breakpoint hits surface immediately) and blocking
//     commands wait on the channel.

import (
	"errors"
	"sync"
	"time"

	"github.com/go-delve/delve/service/api"
)

// StopKind classifies why the target stopped.
type StopKind string

const (
	StopBreakpoint StopKind = "breakpoint"
	StopStep       StopKind = "step"
	StopHalted     StopKind = "halted"
	StopExited     StopKind = "exited"
	StopTimeout    StopKind = "timeout"
	StopUnknown    StopKind = "unknown"
)

// StopInfo describes one stop of the target.
type StopInfo struct {
	Kind    StopKind
	State   *api.DebuggerState
	BP      *BPView // breakpoint hit (Kind == StopBreakpoint)
	Exited  bool
	Status  int
	Message string
}

// Where returns the file/line/function the target stopped at.
func (si *StopInfo) Where() (file string, line int, fn string) {
	if si == nil || si.State == nil || si.State.CurrentThread == nil {
		return "", 0, ""
	}
	t := si.State.CurrentThread
	if t.Function != nil {
		fn = t.Function.Name()
	}
	return t.File, t.Line, fn
}

// postedStop wraps a StopInfo with the generation it was issued under.
type postedStop struct {
	si  *StopInfo
	gen uint64
}

type control struct {
	sess   *Session
	mu     sync.Mutex
	gen    uint64
	stopCh chan postedStop
	waitCh chan struct{}
	cancel bool
}

func newControl(s *Session) *control {
	return &control{sess: s, stopCh: make(chan postedStop, 8)}
}

// currentGen returns the active generation.
func (c *control) currentGen() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// halt raises the generation and stops the target if it is running.
// Returns whether the target was running (and is now stopped).
func (c *control) halt() (bool, error) {
	c.mu.Lock()
	c.gen++
	c.mu.Unlock()
	if !c.sess.Running() {
		return false, nil
	}
	st, err := c.sess.cli.Halt()
	if err != nil {
		return true, err
	}
	_ = st
	return true, nil
}

// startRun issues a continue in the background if the target is stopped.
// The stop it produces is posted under the current generation.
func (c *control) startRun() {
	if c.sess.Running() {
		return
	}
	gen := c.currentGen()
	go func() {
		ch := c.sess.cli.Continue()
		st, ok := <-ch
		if !ok {
			c.post(postedStop{si: &StopInfo{Kind: StopUnknown, Message: "debug connection closed"}, gen: gen})
			return
		}
		c.post(postedStop{si: classifyStop(st, nil, "continue"), gen: gen})
	}()
}

// runStep executes a synchronous stepping command (next/step/stepOut) and
// posts its stop under the current generation.
func (c *control) runStep(name string) {
	gen := c.currentGen()
	go func() {
		var st *api.DebuggerState
		var err error
		switch name {
		case "next":
			st, err = c.sess.cli.Next()
		case "step":
			st, err = c.sess.cli.Step()
		case "stepOut":
			st, err = c.sess.cli.StepOut()
		default:
			err = nil
		}
		si := classifyStop(st, err, name)
		if err != nil && si.Message == "" {
			si.Message = err.Error()
		}
		c.post(postedStop{si: si, gen: gen})
	}()
}

func (c *control) post(p postedStop) {
	if p.gen < c.currentGen() {
		return // stale: produced by an execution we interrupted
	}
	if p.si.Kind == StopExited {
		c.sess.noteExit(p.si.Status)
	}
	select {
	case c.stopCh <- p:
		return
	default:
		select {
		case <-c.stopCh: // clear one stale slot
		default:
		}
		select {
		case c.stopCh <- p:
		default:
		}
	}
}

// PollStop returns the next pending stop without blocking, skipping stops
// from generations we already invalidated.
func (c *control) PollStop() (*StopInfo, bool) {
	gen := c.currentGen()
	for {
		select {
		case p := <-c.stopCh:
			if p.gen < gen {
				continue
			}
			return p.si, true
		default:
			return nil, false
		}
	}
}

// RunToStop runs a blocking execution command: it halts the target first if
// it was running, issues the command in the background, then waits for the
// stop it produces.
//
// On timeout the target is halted again before returning: a timed-out wait
// must never leave an unowned continue running on the server (it would hold
// the debugger lock until the next breakpoint, blocking every later RPC).
func (c *control) RunToStop(name string, timeout time.Duration) *StopInfo {
	if c.cancelRequested() {
		return &StopInfo{Kind: StopHalted, Message: "interrupted"}
	}
	if _, err := c.halt(); err != nil {
		return classifyStop(nil, err, name)
	}
	gen := c.currentGen()
	switch name {
	case "continue":
		c.startRun()
	case "next", "step", "stepOut":
		c.runStep(name)
	default:
		return &StopInfo{Kind: StopUnknown, Message: "unknown execution command " + name}
	}
	return c.waitStop(gen, timeout)
}

// waitStop waits for a stop of the given generation (or later ones that are
// not stale).
func (c *control) waitStop(gen uint64, timeout time.Duration) *StopInfo {
	c.mu.Lock()
	c.waitCh = make(chan struct{})
	intCh := c.waitCh
	c.mu.Unlock()

	var tc <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		tc = t.C
	}
	for {
		select {
		case p := <-c.stopCh:
			if p.gen < gen {
				continue // stop of an interrupted command: keep waiting
			}
			return p.si
		case <-tc:
			// Bring the target back to a deterministic stopped state so no
			// unowned continue keeps the server busy.
			c.halt()
			return &StopInfo{Kind: StopTimeout, Message: "no stop within " + timeout.String()}
		case <-intCh:
			return &StopInfo{Kind: StopHalted, Message: "interrupted"}
		}
	}
}

// Interrupt stops the target (if running), runs fn while it is stopped and
// resumes it if it was running. All commands that need a quiesced target
// (breakpoint management, inspection) run inside Interrupt.
func (c *control) Interrupt(fn func() error) error {
	if c.cancelRequested() {
		return errors.New("session interrupted")
	}
	halted, err := c.halt()
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	if halted {
		c.startRun()
	}
	return nil
}

// cancelWait arms the interrupt for the current and future waits.
func (c *control) cancelWait() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel = true
	if c.waitCh != nil {
		close(c.waitCh)
		c.waitCh = nil
	}
}

func (c *control) cancelRequested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancel
}

func classifyStop(st *api.DebuggerState, err error, cmdName string) *StopInfo {
	si := &StopInfo{State: st}
	if st == nil {
		if err != nil && isProcessExited(err) {
			si.Kind = StopExited
			si.Exited = true
		} else if err != nil {
			si.Kind = StopUnknown
			si.Message = err.Error()
		} else {
			si.Kind = StopHalted
		}
		return si
	}
	if st.Exited {
		si.Kind = StopExited
		si.Exited = true
		si.Status = st.ExitStatus
		return si
	}
	if st.CurrentThread != nil && st.CurrentThread.Breakpoint != nil {
		si.Kind = StopBreakpoint
		b := st.CurrentThread.Breakpoint
		si.BP = &BPView{ID: b.ID, Name: b.Name, File: b.File, Line: b.Line, Function: b.FunctionName,
			Cond: b.Cond, HitCond: b.HitCond, Trace: b.Tracepoint, Disabled: b.Disabled,
			HitCount: b.HitCount, TotalHitCount: b.TotalHitCount}
		return si
	}
	switch {
	case st.StopReason == "breakpoint" || st.StopReason == "breakpoint set":
		si.Kind = StopBreakpoint
	case cmdName == "next" || cmdName == "step" || cmdName == "stepOut":
		si.Kind = StopStep
	case cmdName == "continue":
		si.Kind = StopHalted // continue returned without hitting anything
	default:
		si.Kind = StopHalted
	}
	return si
}
