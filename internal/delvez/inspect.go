package delvez

// Inspection over the remote debugger: stack frames, locals/args, expression
// evaluation, goroutines and source snippets. Queries run inside Interrupt so
// they work both from a stop and while the target serves.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-delve/delve/service/api"

	"github.com/adamancyzhang/agent-go-debugger/internal/format"
)

// loadConfig bounds how much of a value the server reads from target memory,
// so a single request stays cheap on live services.
func loadConfig() api.LoadConfig {
	return api.LoadConfig{
		FollowPointers:     true,
		MaxVariableRecurse: 2,
		MaxStringLen:       256,
		MaxArrayValues:     32,
		MaxStructFields:    16,
	}
}

// Frame is the display projection of one stack frame.
type Frame struct {
	Index    int    `json:"index"`
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	PC       uint64 `json:"pc,omitempty"`
}

// GoroutineView is the display projection of one goroutine.
type GoroutineView struct {
	ID         int64  `json:"id"`
	Status     string `json:"status,omitempty"`
	ThreadID   int    `json:"threadID,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Function   string `json:"function,omitempty"`
	WaitReason string `json:"waitReason,omitempty"`
	Unreadable string `json:"unreadable,omitempty"`
}

// scope builds the eval scope for the given goroutine and frame.
func (s *Session) scope(goid int64, frame int) api.EvalScope {
	return api.EvalScope{GoroutineID: goid, Frame: frame}
}

// CurrentGoroutineID returns the selected goroutine id at the stop.
func (s *Session) CurrentGoroutineID() (int64, bool) {
	st, err := s.State(true)
	if err != nil || st == nil || st.SelectedGoroutine == nil {
		return 0, false
	}
	return st.SelectedGoroutine.ID, true
}

// Stack returns frames of a goroutine (goid<=0 means current). depth<=0 -> 20.
func (s *Session) Stack(goid int64, depth int, withVars bool) ([]Frame, error) {
	if depth <= 0 {
		depth = 20
	}
	var out []Frame
	err := s.Interrupt(func() error {
		cfg := loadConfig()
		frames, err := s.cli.Stacktrace(goid, depth, 0, 0, &cfg)
		if err != nil {
			return err
		}
		for i := range frames {
			f := Frame{
				Index:    i,
				Function: frames[i].Function.Name(),
				File:     frames[i].File,
				Line:     frames[i].Line,
				PC:       frames[i].PC,
			}
			out = append(out, f)
		}
		return nil
	})
	return out, err
}

// Locals returns local variables of a frame on the current goroutine.
func (s *Session) Locals(goid int64, frame int) ([]format.VarView, error) {
	return s.frameVars(goid, frame, false)
}

// Args returns function arguments of a frame on the current goroutine.
func (s *Session) Args(goid int64, frame int) ([]format.VarView, error) {
	return s.frameVars(goid, frame, true)
}

func (s *Session) frameVars(goid int64, frame int, args bool) ([]format.VarView, error) {
	var out []format.VarView
	err := s.Interrupt(func() error {
		cfg := loadConfig()
		var vars []api.Variable
		var err error
		if args {
			vars, err = s.cli.ListFunctionArgs(s.scope(goid, frame), cfg)
		} else {
			vars, err = s.cli.ListLocalVariables(s.scope(goid, frame), cfg)
		}
		if err != nil {
			return err
		}
		out = varsToViews(vars)
		return nil
	})
	return out, err
}

// Eval evaluates an expression in the given goroutine's frame.
func (s *Session) Eval(goid int64, frame int, expr string) (format.VarView, error) {
	var out format.VarView
	err := s.Interrupt(func() error {
		v, err := s.cli.EvalVariable(s.scope(goid, frame), expr, loadConfig())
		if err != nil {
			return err
		}
		out = varView(v)
		return nil
	})
	return out, err
}

// Goroutines lists all goroutines (paged at 512).
func (s *Session) Goroutines() ([]GoroutineView, int64, error) {
	var out []GoroutineView
	var selected int64
	err := s.Interrupt(func() error {
		gs, _, err := s.cli.ListGoroutines(0, 512)
		if err != nil {
			return err
		}
		for _, g := range gs {
			out = append(out, goroutineView(g))
		}
		if st, err := s.State(true); err == nil && st != nil && st.SelectedGoroutine != nil {
			selected = st.SelectedGoroutine.ID
		}
		return nil
	})
	return out, selected, err
}

// goroutineView projects an api.Goroutine.
func goroutineView(g *api.Goroutine) GoroutineView {
	v := GoroutineView{ID: g.ID, ThreadID: g.ThreadID, Unreadable: g.Unreadable}
	loc := g.CurrentLoc
	if loc.File != "" {
		v.File = loc.File
		v.Line = loc.Line
		if loc.Function != nil {
			v.Function = loc.Function.Name()
		}
	}
	switch {
	case g.Status == 1: // _Gwaiting
		v.Status = "waiting"
	case g.Status == 2: // _Grunnable
		v.Status = "runnable"
	case g.Status == 3: // _Grunning
		v.Status = "running"
	case g.Status == 4: // _Gsyscall
		v.Status = "syscall"
	default:
		v.Status = fmt.Sprintf("gstatus-%d", g.Status)
	}
	if g.WaitReason != 0 {
		v.WaitReason = fmt.Sprintf("%d", g.WaitReason)
	}
	return v
}

// SourceLines reads a source window around file:line for display.
func SourceLines(file string, line, before, after int) []SourceLine {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	lines, err := readAllLines(f)
	if err != nil {
		return nil
	}
	lo := line - before
	if lo < 1 {
		lo = 1
	}
	hi := line + after
	if hi > len(lines) {
		hi = len(lines)
	}
	var out []SourceLine
	for i := lo; i <= hi; i++ {
		out = append(out, SourceLine{N: i, Text: lines[i-1], Current: i == line})
	}
	return out
}

// SourceLine is one line of source context.
type SourceLine struct {
	N       int    `json:"n"`
	Text    string `json:"text"`
	Current bool   `json:"current,omitempty"`
}

func readAllLines(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	raw := strings.Split(string(data), "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	return raw, nil
}

// varsToViews projects api.Variable list (already string-valued).
func varsToViews(vars []api.Variable) []format.VarView {
	out := make([]format.VarView, 0, len(vars))
	for i := range vars {
		out = append(out, varView(&vars[i]))
	}
	return out
}

// varView projects one api.Variable.
func varView(v *api.Variable) format.VarView {
	if v == nil {
		return format.VarView{Value: "<nil>"}
	}
	out := format.VarView{
		Name:       v.Name,
		Type:       v.Type,
		Kind:       v.Kind.String(),
		Value:      v.Value,
		Unreadable: v.Unreadable,
		Len:        v.Len,
		Cap:        v.Cap,
	}
	for i := range v.Children {
		out.Children = append(out.Children, varView(&v.Children[i]))
	}
	return out
}
