package console

import (
	"strconv"
	"strings"

	"github.com/adamancyzhang/agent-go-debugger/internal/delvez"
	"github.com/adamancyzhang/agent-go-debugger/internal/format"
)

// --- breakpoints ---------------------------------------------------------------

// cmdBp handles: add/list/remove/enable/disable/clear/condition.
func (r *Runner) cmdBp(args []string) {
	if len(args) == 0 || args[0] == "list" {
		r.bpList()
		return
	}
	switch args[0] {
	case "add", "set", "a":
		r.bpAdd(args[1:])
	case "remove", "del", "rm", "delete":
		r.bpRefOp(args[1:], "remove")
	case "enable", "on":
		r.bpRefOp(args[1:], "enable")
	case "disable", "off":
		r.bpRefOp(args[1:], "disable")
	case "clear":
		if err := r.s.ClearAllBreakpoints(); err != nil {
			r.errorf("clear breakpoints: %v", err)
			return
		}
		r.emit("bp", map[string]any{"op": "clear", "ok": true})
	case "condition", "cond":
		r.bpCond(args[1:])
	default:
		r.errorf("unknown bp subcommand %q (add|list|remove|enable|disable|clear|condition)", args[0])
	}
}

func (r *Runner) bpList() {
	bps, err := r.s.ListBreakpoints()
	if err != nil {
		r.errorf("list breakpoints: %v", err)
		return
	}
	items := make([]map[string]any, 0, len(bps))
	for _, b := range bps {
		items = append(items, bpObj(b))
	}
	r.emit("bp", map[string]any{"op": "list", "items": items})
}

func (r *Runner) bpAdd(args []string) {
	spec := delvez.BpSpec{}
	var pos []string
	once := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--file":
			i++
			if i < len(args) {
				spec.File = args[i]
			}
		case a == "--function":
			i++
			if i < len(args) {
				spec.Function = args[i]
			}
		case a == "--line":
			i++
			if i < len(args) {
				spec.Line, _ = strconv.Atoi(args[i])
			}
		case a == "--name":
			i++
			if i < len(args) {
				spec.Name = args[i]
			}
		case a == "--cond":
			i++
			if i < len(args) {
				spec.Cond = args[i]
			}
		case a == "--hit-expr":
			i++
			if i < len(args) {
				spec.HitCond = args[i]
			}
		case a == "--trace":
			spec.Trace = true
		case a == "--once":
			once = true
		case a == "--disabled":
			spec.Disabled = true
		default:
			pos = append(pos, a)
		}
	}
	// positional forms: "main.go:38" or "--file main.go 38"
	for _, p := range pos {
		if spec.File == "" && spec.Function == "" {
			if file, line, ok := strings.Cut(p, ":"); ok {
				if ln, err := strconv.Atoi(line); err == nil && file != "" {
					spec.File, spec.Line = file, ln
					continue
				}
			}
		}
		if spec.Line == 0 && spec.File != "" && spec.Function == "" {
			if ln, err := strconv.Atoi(p); err == nil {
				spec.Line = ln
				continue
			}
		}
		if spec.Function == "" && spec.File == "" {
			spec.Function = p
			continue
		}
		r.errorf("unexpected argument %q", p)
		return
	}
	if spec.Function == "" && (spec.File == "" || spec.Line == 0) {
		r.errorf("usage: bp add --file PATH --line N | --function NAME [--cond EXPR] [--name N] [--trace] [--once] [--hit-expr '>N']")
		return
	}
	bp, err := r.s.AddBreakpoint(spec)
	if err != nil {
		r.errorf("add breakpoint: %v", err)
		return
	}
	if once {
		r.once[bp.ID] = true
	}
	bp.Trace = spec.Trace
	r.emit("bp", map[string]any{"op": "add", "bp": bpObj(bp), "once": once})
}

func (r *Runner) bpRefOp(args []string, op string) {
	if len(args) == 0 {
		r.errorf("usage: bp %s <id|name>", op)
		return
	}
	id, err := r.s.ResolveBPRef(args[0])
	if err != nil {
		r.errorf("bp %s: %v", op, err)
		return
	}
	switch op {
	case "remove":
		if err := r.s.RemoveBreakpoint(id); err != nil {
			r.errorf("bp remove: %v", err)
			return
		}
		delete(r.once, id)
	case "enable":
		if err := r.s.SetBreakpointEnabled(id, true); err != nil {
			r.errorf("bp enable: %v", err)
			return
		}
	case "disable":
		if err := r.s.SetBreakpointEnabled(id, false); err != nil {
			r.errorf("bp disable: %v", err)
			return
		}
	}
	r.emit("bp", map[string]any{"op": op, "id": id, "ref": args[0], "ok": true})
}

func (r *Runner) bpCond(args []string) {
	if len(args) < 2 {
		r.errorf("usage: bp condition <id|name> EXPR  (empty EXPR clears)")
		return
	}
	id, err := r.s.ResolveBPRef(args[0])
	if err != nil {
		r.errorf("bp condition: %v", err)
		return
	}
	cond := strings.Join(args[1:], " ")
	if err := r.s.SetBreakpointCondition(id, cond); err != nil {
		r.errorf("bp condition: %v", err)
		return
	}
	r.emit("bp", map[string]any{"op": "condition", "id": id, "cond": cond, "ok": true})
}

// --- stack / frames ---------------------------------------------------------------

func (r *Runner) cmdStack(args []string) {
	depth := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--depth":
			i++
			if i < len(args) {
				depth, _ = strconv.Atoi(args[i])
			}
		}
	}
	frames, err := r.s.Stack(r.curGoid(), depth, false)
	if err != nil {
		r.errorf("stack: %v", err)
		return
	}
	items := make([]map[string]any, 0, len(frames))
	for _, f := range frames {
		items = append(items, map[string]any{
			"index": f.Index, "function": f.Function, "file": f.File, "line": f.Line,
		})
	}
	r.emit("stack", map[string]any{"frames": items})
}

// --- locals / args ------------------------------------------------------------------

func (r *Runner) cmdLocals(args []string, isArgs bool) {
	frame := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--frame":
			i++
			if i < len(args) {
				frame, _ = strconv.Atoi(args[i])
			}
		}
	}
	kind := "locals"
	if isArgs {
		kind = "args"
	}
	var vars []format.VarView
	var err error
	if isArgs {
		vars, err = r.s.Args(r.curGoid(), frame)
	} else {
		vars, err = r.s.Locals(r.curGoid(), frame)
	}
	if err != nil {
		r.errorf("%s: %v", kind, err)
		return
	}
	r.emit(kind, map[string]any{"frame": frame, "vars": vars})
}

// --- eval / print ---------------------------------------------------------------------

func (r *Runner) cmdEval(args []string) {
	if len(args) == 0 {
		r.errorf("usage: eval EXPR [--frame N]")
		return
	}
	frame := 0
	exprParts := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--frame" {
			i++
			if i < len(args) {
				frame, _ = strconv.Atoi(args[i])
			}
			continue
		}
		exprParts = append(exprParts, args[i])
	}
	expr := strings.Join(exprParts, " ")
	v, err := r.s.Eval(r.curGoid(), frame, expr)
	if err != nil {
		r.errorf("eval %q: %v", expr, err)
		return
	}
	r.emit("eval", map[string]any{"expression": expr, "frame": frame, "value": v})
}

// --- goroutines --------------------------------------------------------------------------

func (r *Runner) cmdGoroutines() {
	gs, selected, err := r.s.Goroutines()
	if err != nil {
		r.errorf("goroutines: %v", err)
		return
	}
	items := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		items = append(items, map[string]any{
			"id": g.ID, "status": g.Status, "threadID": g.ThreadID,
			"file": g.File, "line": g.Line, "function": g.Function,
			"waitReason": g.WaitReason,
			"selected":   g.ID == selected,
		})
	}
	r.emit("goroutines", map[string]any{"selected": selected, "items": items})
}

func (r *Runner) cmdGoroutine(args []string) {
	if len(args) == 0 {
		r.errorf("usage: goroutine <id>")
		return
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		r.errorf("goroutine: invalid id %q", args[0])
		return
	}
	if err := r.s.SwitchGoroutine(id); err != nil {
		r.errorf("goroutine: %v", err)
		return
	}
	r.goid, r.goidSet = id, true
	r.emit("goroutine", map[string]any{"id": id})
}

// --- source -------------------------------------------------------------------------------

func (r *Runner) cmdSource(args []string) {
	file, line := "", 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--file" && i+1 < len(args) {
			i++
			file = args[i]
			continue
		}
		if a == "--line" && i+1 < len(args) {
			i++
			line, _ = strconv.Atoi(args[i])
			continue
		}
		if strings.Contains(a, ":") && file == "" {
			if f, l, ok := strings.Cut(a, ":"); ok {
				if ln, err := strconv.Atoi(l); err == nil {
					file, line = f, ln
					continue
				}
			}
		}
		r.errorf("usage: source <file:line> | --file F --line N")
		return
	}
	if file == "" || line == 0 {
		// default: current stop location
		file, line, _ = r.currentLoc()
	}
	if file == "" {
		r.errorf("no current location; pass file:line explicitly")
		return
	}
	r.emit("source", map[string]any{
		"file": file, "line": line, "source": sourceWindow(file, line),
	})
}

func (r *Runner) cmdHelp() {
	r.emit("help", map[string]any{"text": helpText})
}

var helpText = `commands:
  bp add --file PATH --line N | --function NAME   set a breakpoint
       [--cond EXPR] [--name N] [--trace] [--once] [--hit-expr '>N'] [--disabled]
  bp list | bp remove <id|name> | bp enable <id|name> | bp disable <id|name>
  bp clear | bp condition <id|name> EXPR
  continue | c      run until next stop        next | n       step over
  step | s          step into                   fin | stepout   step out
  halt              pause a running target     resume         alias for continue
  goroutines | gs    list goroutines            goroutine <id> switch current
  stack | bt         stack frames (current goroutine)
  locals [--frame N]  local variables           args [--frame N] arguments
  eval|print EXPR     evaluate expression       source [file:line]
  status              target state              trace          flush trace hits
  quit | exit         detach and leave target running
  kill                terminate target (exec mode only)
  help                this text`
