// Package format renders inspected values and locations for both the human
// and the --json output paths. Data arrives as VarView, a display projection
// produced by the delvez package, so this package never imports Delve.
package format

import (
	"encoding/json"
	"fmt"
)

// VarView is a display projection of a debugger variable.
type VarView struct {
	Name       string    `json:"name,omitempty"`
	Type       string    `json:"type,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Value      string    `json:"value"`
	Unreadable string    `json:"unreadable,omitempty"`
	Len        int64     `json:"len,omitempty"`
	Cap        int64     `json:"cap,omitempty"`
	Children   []VarView `json:"children,omitempty"`
}

// OneLine renders a variable as a single human-readable string:
//
//	msg (string) "hello doubled 2 -> 4"
//	rows ([]int) [1, 2, 3] (len=3)
//	err (error) <unreadable: ...>
func OneLine(v VarView) string {
	value := scalar(v)
	if v.Name != "" {
		value = fmt.Sprintf("%s (%s) %s", v.Name, v.Type, value)
	}
	return value
}

// scalar renders the value part of a variable.
func scalar(v VarView) string {
	if v.Unreadable != "" {
		return "<unreadable: " + v.Unreadable + ">"
	}
	if v.Value == "" {
		switch v.Kind {
		case "String", "Slice", "Array", "Map", "Struct", "Ptr", "Interface", "Chan", "Func":
			return fmt.Sprintf("(%s) len=%d", v.Type, v.Len)
		}
		return "?"
	}
	return v.Value
}

// Tree renders a variable recursively as indented lines (inspect view).
func Tree(v VarView, depth int) []string {
	head := OneLine(v)
	if depth <= 0 || len(v.Children) == 0 {
		return []string{head}
	}
	// pointer/interface dereference reads naturally one level up
	if len(v.Children) == 1 && (v.Kind == "Ptr" || v.Kind == "Interface") {
		c := v.Children[0]
		c.Name = "*" + v.Name
		return Tree(c, depth-1)
	}
	out := []string{head}
	for _, c := range v.Children {
		for _, l := range Tree(c, depth-1) {
			out = append(out, "  "+l)
		}
	}
	return out
}

// JSON renders any value as a single-line JSON object.
func JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"type":"error","message":"json encode failed"}`
	}
	return string(b)
}

// LocLine renders "file:line (in function)".
func LocLine(file string, line int, fn string) string {
	s := fmt.Sprintf("%s:%d", file, line)
	if fn != "" {
		s += " (in " + fn + ")"
	}
	return s
}
