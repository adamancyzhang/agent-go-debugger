package delvez

// Breakpoint management over the remote debugger. All operations run inside
// Interrupt so they work whether the target is stopped at a breakpoint or
// serving normally.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-delve/delve/service/api"
)

// ErrNotFound wraps an unknown breakpoint id/name.
var ErrNotFound = errors.New("breakpoint not found")

// BPView is the display projection of a registered breakpoint.
type BPView struct {
	ID            int               `json:"id"`
	Name          string            `json:"name,omitempty"`
	File          string            `json:"file,omitempty"`
	Line          int               `json:"line,omitempty"`
	Function      string            `json:"function,omitempty"`
	Cond          string            `json:"cond,omitempty"`
	HitCond       string            `json:"hitCond,omitempty"`
	Trace         bool              `json:"trace,omitempty"`
	Disabled      bool              `json:"disabled,omitempty"`
	HitCount      map[string]uint64 `json:"hitCount,omitempty"`
	TotalHitCount uint64            `json:"totalHitCount"`
}

func bpView(b *api.Breakpoint) BPView {
	return BPView{
		ID: b.ID, Name: b.Name, File: b.File, Line: b.Line,
		Function: b.FunctionName, Cond: b.Cond, HitCond: b.HitCond,
		Trace: b.Tracepoint, Disabled: b.Disabled,
		HitCount: b.HitCount, TotalHitCount: b.TotalHitCount,
	}
}

// BpSpec describes where and how to set a breakpoint.
type BpSpec struct {
	File     string // file path + Line
	Line     int
	Function string // fully qualified function name
	Name     string // user-facing name (optional)
	Cond     string // condition expression
	HitCond  string // hit-count condition, e.g. "> 3"
	Trace    bool   // tracepoint: log hit, do not pause
	Disabled bool   // create disabled
}

// AddBreakpoint sets a breakpoint from spec and returns its projection.
func (s *Session) AddBreakpoint(spec BpSpec) (BPView, error) {
	req := &api.Breakpoint{
		Name:       spec.Name,
		Cond:       spec.Cond,
		HitCond:    spec.HitCond,
		Tracepoint: spec.Trace,
		Disabled:   spec.Disabled,
	}
	if spec.Function != "" {
		req.FunctionName = spec.Function
	} else {
		req.File = spec.File
		req.Line = spec.Line
	}
	var bp *api.Breakpoint
	err := s.Interrupt(func() error {
		var e error
		bp, e = s.cli.CreateBreakpoint(req)
		if e != nil && req.File != "" && looksLikePathMiss(req.File, e) {
			bp, e = s.cli.CreateBreakpoint(req) // server applies its own path rules
		}
		return e
	})
	if err != nil {
		if spec.Function != "" && strings.Contains(err.Error(), "could not find function") {
			return BPView{}, fmt.Errorf("%v (hint: function breakpoints need the full name with package path, e.g. github.com/org/repo/package.Func)", err)
		}
		if spec.Function == "" && strings.Contains(err.Error(), "could not find file") {
			return BPView{}, fmt.Errorf("%v (hint: file paths are matched against the binary's build-time paths — an absolute path usually works)", err)
		}
		return BPView{}, err
	}
	return bpView(bp), nil
}

// ListBreakpoints returns all breakpoints registered in the target.
func (s *Session) ListBreakpoints() ([]BPView, error) {
	var out []BPView
	err := s.Interrupt(func() error {
		bps, e := s.cli.ListBreakpoints(true)
		if e != nil {
			return e
		}
		for _, b := range bps {
			out = append(out, bpView(b))
		}
		return nil
	})
	return out, err
}

// ResolveBPRef maps a numeric id or a user-given name to a breakpoint id.
func (s *Session) ResolveBPRef(ref string) (int, error) {
	if id, err := strconv.Atoi(ref); err == nil {
		return id, nil
	}
	id := -1
	err := s.Interrupt(func() error {
		bp, e := s.cli.GetBreakpointByName(ref)
		if e != nil || bp == nil {
			return ErrNotFound
		}
		id = bp.ID
		return nil
	})
	if err != nil {
		return 0, err
	}
	if id < 0 {
		return 0, ErrNotFound
	}
	return id, nil
}

// RemoveBreakpoint deletes a breakpoint by dlv id.
func (s *Session) RemoveBreakpoint(id int) error {
	return s.Interrupt(func() error {
		_, err := s.cli.ClearBreakpoint(id)
		return err
	})
}

// ClearAllBreakpoints removes every breakpoint.
func (s *Session) ClearAllBreakpoints() error {
	return s.Interrupt(func() error {
		bps, err := s.cli.ListBreakpoints(true)
		if err != nil {
			return err
		}
		var firstErr error
		for _, b := range bps {
			if _, err := s.cli.ClearBreakpoint(b.ID); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	})
}

// SetBreakpointEnabled disables/enables a breakpoint. ToggleBreakpoint flips
// the state, so we first read the current state and only toggle when it
// differs from the requested one.
func (s *Session) SetBreakpointEnabled(id int, enabled bool) error {
	return s.Interrupt(func() error {
		bp, err := s.cli.GetBreakpoint(id)
		if err != nil || bp == nil {
			return ErrNotFound
		}
		if bp.Disabled != enabled {
			return nil // already in the requested state
		}
		_, err = s.cli.ToggleBreakpoint(id)
		return err
	})
}

// SetBreakpointCondition updates a breakpoint's condition expression
// (empty string clears it).
func (s *Session) SetBreakpointCondition(id int, cond string) error {
	return s.Interrupt(func() error {
		bp, err := s.cli.GetBreakpoint(id)
		if err != nil || bp == nil {
			return ErrNotFound
		}
		clone := *bp
		clone.Cond = cond
		return s.cli.AmendBreakpoint(&clone)
	})
}

// looksLikePathMiss classifies location-resolution errors that a source-path
// mismatch could explain.
func looksLikePathMiss(file string, err error) bool {
	if file == "" || err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, frag := range []string{"no source file", "could not find", "file not found", "not found", "location not found"} {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}
