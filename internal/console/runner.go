// Package console implements the command runner shared by --exec scripts, the
// REPL and the persistent (--hold --in fifo) mode. It renders events in human
// text or as JSONL, applies the stop-hold guard, and computes exit codes.
package console

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/adamancyzhang/agent-go-debugger/internal/delvez"
	"github.com/adamancyzhang/agent-go-debugger/internal/format"
)

// Options configures one console run.
type Options struct {
	JSON        bool
	CmdTimeout  time.Duration // per blocking-command wait; 0 = wait forever
	ResumeAfter time.Duration // stop-hold guard; 0 disables auto-resume
}

// DefaultResumeAfter mirrors agent-java-debugger's stop-hold default.
const DefaultResumeAfter = 60 * time.Second

// Runner executes commands against a session.
type Runner struct {
	s       *delvez.Session
	o       Options
	out     io.Writer
	errw    io.Writer
	fail    int
	once    map[int]bool // dlv ids of --once breakpoints (auto-remove on hit)
	goid    int64
	goidSet bool
	guard   *time.Timer
	done    string // "" | "detach" | "kill" | "exited" | "usage"
}

// New creates a Runner.
func New(s *delvez.Session, o Options, out, errw io.Writer) *Runner {
	return &Runner{s: s, o: o, out: out, errw: errw, once: map[int]bool{}}
}

// ExitCode: 0 ok, 1 any command failure / wait timeout, 2 usage,
// 130 interrupted by a signal.
func (r *Runner) ExitCode() int {
	if r.done == "usage" {
		return 2
	}
	if r.done == "interrupt" {
		return 130
	}
	if r.fail > 0 {
		return 1
	}
	return 0
}

// Abort marks the session interrupted (SIGINT): the loop exits and blocking
// waits return. The target is detached by the caller.
func (r *Runner) Abort() {
	if r.done == "" {
		r.done = "interrupt"
	}
	r.s.CancelWait()
	r.disarmGuard()
}

// --- event emission ---------------------------------------------------------

// emit writes one event: a JSON line in --json mode, human text otherwise.
func (r *Runner) emit(kind string, obj map[string]any) {
	obj["type"] = kind
	if r.o.JSON {
		fmt.Fprintln(r.out, format.JSON(obj))
		return
	}
	fmt.Fprintln(r.out, humanEvent(kind, obj))
}

func (r *Runner) errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.emit("error", map[string]any{"message": msg})
	r.fail++
}

// --- stop-hold guard ----------------------------------------------------------

// armGuard starts the stop-hold timer: without a command within ResumeAfter
// the target is auto-resumed so a dead agent cannot freeze it.
func (r *Runner) armGuard() {
	if r.o.ResumeAfter <= 0 || r.guard != nil {
		return
	}
	r.guard = time.AfterFunc(r.o.ResumeAfter, func() {
		if r.done != "" {
			return
		}
		r.disarmGuard()
		r.s.RunToStop("continue", 0) // auto-resume
		r.emit("error", map[string]any{
			"message": fmt.Sprintf("stop held for %s without a command — target auto-resumed", r.o.ResumeAfter),
		})
	})
}

// disarmGuard cancels the stop-hold timer.
func (r *Runner) disarmGuard() {
	if r.guard != nil {
		r.guard.Stop()
		r.guard = nil
	}
}

// --- main loops ----------------------------------------------------------------

// RunScript executes --exec commands in order, then either detaches or, in
// hold mode, keeps the session alive reading from in.
func (r *Runner) RunScript(cmds []string, hold bool, in io.Reader) int {
	target := r.s.ExePath()
	if target == "" {
		target = r.s.Addr() // remote session
	}
	r.emit("attach", map[string]any{
		"mode": r.s.Mode(), "pid": r.s.Pid(), "target": target,
	})
	for _, line := range cmds {
		if r.done != "" {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r.emit("command", map[string]any{"line": line})
		if r.dispatch(line) {
			break
		}
	}
	if !hold {
		if r.done == "" {
			r.cmdQuit()
		}
		return r.ExitCode()
	}
	if r.done == "" {
		r.emit("handoff", map[string]any{"state": r.stateWord()})
	}
	return r.loop(in)
}

// loop is the interactive / persistent command loop.
func (r *Runner) loop(in io.Reader) int {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		if r.done != "" {
			break
		}
		if r.pollStop() {
			continue
		}
		if !sc.Scan() {
			break // EOF
		}
		r.disarmGuard()
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if r.o.JSON {
			r.emit("command", map[string]any{"line": line})
		}
		if r.dispatch(line) {
			break
		}
	}
	if r.done == "" && r.s.Mode() == "exec" {
		// EOF with the target alive: detach so the service keeps serving.
		r.cmdQuit()
	}
	return r.ExitCode()
}

// stateWord reports "stopped"/"running" for handoff events.
func (r *Runner) stateWord() string {
	if r.s.Running() {
		return "running"
	}
	return "stopped"
}

// pollStop drains a pending stop and presents it. True when a stop was
// presented (loop should re-poll).
func (r *Runner) pollStop() bool {
	si, ok := r.s.PollStop()
	if !ok {
		return false
	}
	r.presentStop(si)
	return true
}

// presentStop renders a stop delivered by an execution command.
func (r *Runner) presentStop(si *delvez.StopInfo) {
	r.disarmGuard()
	switch si.Kind {
	case delvez.StopExited:
		r.handleExited(si)
		return
	case delvez.StopTimeout:
		r.emit("timeout", map[string]any{"message": si.Message})
		return
	case delvez.StopUnknown:
		r.errorf("%s", si.Message)
		return
	case delvez.StopHalted:
		file, line, fn := si.Where()
		r.emit("stop", map[string]any{"reason": "halted", "file": file, "line": line, "function": fn})
		return
	}
	// breakpoint / step
	var bp any
	if si.BP != nil {
		bp = bpObj(*si.BP)
		if r.once[si.BP.ID] {
			delete(r.once, si.BP.ID)
			if err := r.s.RemoveBreakpoint(si.BP.ID); err != nil {
				r.errorf("remove once breakpoint #%d: %v", si.BP.ID, err)
			}
		}
	}
	file, line, fn := si.Where()
	evt := map[string]any{
		"reason":   string(si.Kind),
		"bp":       bp,
		"file":     file,
		"line":     line,
		"function": fn,
	}
	if file != "" {
		evt["source"] = sourceWindow(file, line)
	}
	r.emit("stop", evt)
	r.armGuard()
}

// handleExited emits the exited event and marks the session done.
func (r *Runner) handleExited(si *delvez.StopInfo) {
	reason := "normal"
	if strings.Contains(si.Message, "panic") {
		reason = "panic"
	}
	r.emit("exited", map[string]any{
		"status": si.Status, "reason": reason,
		"message": mapIfEmpty(si.Message, "process exited"),
	})
	if r.done == "" {
		r.done = "exited"
	}
}

func mapIfEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// bpObj projects a BPView for event payloads.
func bpObj(b delvez.BPView) map[string]any {
	return map[string]any{
		"id": b.ID, "name": b.Name, "file": b.File, "line": b.Line,
		"function": b.Function, "cond": b.Cond, "hitCond": b.HitCond,
		"trace": b.Trace, "disabled": b.Disabled, "totalHitCount": b.TotalHitCount,
	}
}

// --- dispatch ------------------------------------------------------------------

// dispatch runs one command line. Returns true when the session should end.
func (r *Runner) dispatch(line string) bool {
	fields := splitArgs(line)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "help", "?":
		r.cmdHelp()
	case "quit", "exit", "detach", "q":
		r.cmdQuit()
	case "kill":
		r.cmdKill()
	case "continue", "c", "resume":
		r.cmdBlocking("continue")
	case "next", "n":
		r.cmdBlocking("next")
	case "step", "s":
		r.cmdBlocking("step")
	case "stepout", "fin", "finish", "fout":
		r.cmdBlocking("stepOut")
	case "halt", "pause":
		r.cmdHalt()
	case "bp", "b", "breakpoint":
		r.cmdBp(fields[1:])
	case "stack", "bt", "backtrace":
		r.cmdStack(fields[1:])
	case "locals":
		r.cmdLocals(fields[1:], false)
	case "args":
		r.cmdLocals(fields[1:], true)
	case "eval", "print", "p":
		r.cmdEval(fields[1:])
	case "goroutines", "gs":
		r.cmdGoroutines()
	case "goroutine", "g":
		r.cmdGoroutine(fields[1:])
	case "source":
		r.cmdSource(fields[1:])
	case "status":
		r.cmdStatus()
	case "trace":
		r.cmdTrace()
	default:
		r.errorf("unknown command %q (try: help)", fields[0])
	}
	return false
}

func (r *Runner) cmdQuit() {
	if r.done != "" {
		return
	}
	if exited, _ := r.s.Exited(); exited {
		r.done = "exited"
		r.emit("quit", map[string]any{"mode": "exited", "pid": r.s.Pid()})
		return
	}
	if err := r.s.Quit(); err != nil {
		if exited, _ := r.s.Exited(); !exited {
			r.errorf("disconnect: %v", err)
			return
		}
		r.done = "exited"
		r.emit("quit", map[string]any{"mode": "exited", "pid": r.s.Pid()})
		return
	}
	r.done = "disconnect"
	r.emit("quit", map[string]any{"mode": "disconnect", "pid": r.s.Pid()})
}

func (r *Runner) cmdKill() {
	if r.done != "" {
		return
	}
	if err := r.s.Kill(); err != nil {
		r.errorf("kill: %v", err)
		return
	}
	r.done = "kill"
	r.emit("quit", map[string]any{"mode": "kill", "pid": r.s.Pid()})
}

// cmdBlocking runs an execution command (continue/next/step/stepOut).
// Tracepoint hits are reported but do not pause: after emitting the trace
// event the target is continued until a real stop or the timeout budget is
// spent.
func (r *Runner) cmdBlocking(name string) {
	if exited, _ := r.s.Exited(); exited {
		r.errorf("target already exited")
		return
	}
	deadline := time.Time{}
	if r.o.CmdTimeout > 0 {
		deadline = time.Now().Add(r.o.CmdTimeout)
	}
	for {
		var budget time.Duration
		if !deadline.IsZero() {
			budget = time.Until(deadline)
			if budget <= 0 {
				r.emit("timeout", map[string]any{"message": "no stop within " + r.o.CmdTimeout.String()})
				return
			}
		}
		si := r.s.RunToStop(name, budget)
		if si.BP != nil && si.BP.Trace && si.Kind == delvez.StopBreakpoint {
			r.emit("trace", map[string]any{
				"file": si.BP.File, "line": si.BP.Line, "function": si.BP.Function,
				"message": fmt.Sprintf("trace at %s:%d (in %s)", si.BP.File, si.BP.Line, si.BP.Function),
			})
			name = "continue" // keep running across trace hits
			continue
		}
		r.presentStop(si)
		return
	}
}

func (r *Runner) cmdHalt() {
	if !r.s.Running() {
		r.emit("stop", map[string]any{"reason": "halted", "message": "target already stopped"})
		return
	}
	if err := r.s.HaltNow(); err != nil {
		r.errorf("halt: %v", err)
		return
	}
	file, line, fn := r.currentLoc()
	r.emit("stop", map[string]any{"reason": "halted", "file": file, "line": line, "function": fn})
}

// cmdTrace flushes buffered tracepoint hits (also done before each blocking
// command).
func (r *Runner) cmdTrace() {
	r.flushTraces()
}

// flushTraces emits any buffered tracepoint records as trace events.
func (r *Runner) flushTraces() {
	for _, t := range r.s.FlushTraces() {
		r.emit("trace", map[string]any{
			"file": t.File, "line": t.Line, "function": t.Function,
			"message": fmt.Sprintf("trace at %s:%d (in %s)", t.File, t.Line, t.Function),
		})
	}
}

// currentLoc asks the target where it is stopped (best effort).
func (r *Runner) currentLoc() (string, int, string) {
	st, err := r.s.State(true)
	if err != nil || st == nil || st.CurrentThread == nil {
		return "", 0, ""
	}
	fn := ""
	if st.CurrentThread.Function != nil {
		fn = st.CurrentThread.Function.Name()
	}
	return st.CurrentThread.File, st.CurrentThread.Line, fn
}

func (r *Runner) cmdStatus() {
	exited, status := r.s.Exited()
	state := "running"
	if exited {
		state = fmt.Sprintf("exited(%d)", status)
	} else if !r.s.Running() {
		state = "stopped"
	}
	bps, err := r.s.ListBreakpoints()
	n := -1
	if err == nil {
		n = len(bps)
	}
	r.emit("status", map[string]any{"state": state, "pid": r.s.Pid(), "breakpoints": n})
}

// --- current goroutine / helpers ------------------------------------------------

func (r *Runner) curGoid() int64 {
	if r.goidSet {
		return r.goid
	}
	if id, ok := r.s.CurrentGoroutineID(); ok {
		return id
	}
	return 0
}

// sourceWindow renders lines around file:line.
func sourceWindow(file string, line int) []map[string]any {
	lines := delvez.SourceLines(file, line, 2, 6)
	if lines == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		out = append(out, map[string]any{"n": l.N, "text": l.Text, "current": l.Current})
	}
	return out
}

// splitArgs is a minimal POSIX-ish tokenizer (whitespace split respecting
// single/double quotes).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inS, inD := false, false
	for _, ch := range s {
		switch {
		case ch == '\'' && !inD:
			inS = !inS
		case ch == '"' && !inS:
			inD = !inD
		case (ch == ' ' || ch == '\t') && !inS && !inD:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(ch)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
