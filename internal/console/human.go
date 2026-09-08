package console

// humanEvent renders one event as a human-readable line (used outside --json).
// Unicode markers follow the style of agent-java-debugger.

import (
	"fmt"
	"strings"

	"github.com/adamancyzhang/agent-go-debugger/internal/format"
)

func humanEvent(kind string, obj map[string]any) string {
	switch kind {
	case "attach":
		return fmt.Sprintf("◈ attached (%s) pid=%v target=%q workdir=%q",
			obj["mode"], obj["pid"], obj["target"], obj["workdir"])
	case "command":
		return fmt.Sprintf("» %s", obj["line"])
	case "bp":
		return humanBp(obj)
	case "stop":
		return humanStop(obj)
	case "trace":
		return fmt.Sprintf("✎ %s", obj["message"])
	case "timeout":
		return fmt.Sprintf("⏱ timeout: %s", obj["message"])
	case "exited":
		return fmt.Sprintf("✖ process exited status=%v reason=%s (%s)", obj["status"], obj["reason"], obj["message"])
	case "error":
		return fmt.Sprintf("✗ %s", obj["message"])
	case "goroutines":
		return humanGoroutines(obj)
	case "goroutine":
		return fmt.Sprintf("◈ goroutine %v selected", obj["id"])
	case "stack":
		return humanStack(obj)
	case "locals", "args":
		return humanVars(kind, obj)
	case "eval":
		return humanEval(obj)
	case "source":
		return humanSource(obj)
	case "status":
		return fmt.Sprintf("state: %s  pid=%v  breakpoints=%v", obj["state"], obj["pid"], obj["breakpoints"])
	case "handoff":
		return fmt.Sprintf("◈ session handed off — target %s; send commands via the --in stream", obj["state"])
	case "quit":
		return fmt.Sprintf("◈ quit (mode=%s) pid=%v", obj["mode"], obj["pid"])
	case "help":
		return fmt.Sprint(obj["text"])
	default:
		return fmt.Sprint(obj)
	}
}

func humanBp(obj map[string]any) string {
	op := obj["op"]
	switch op {
	case "add":
		if b, ok := obj["bp"].(map[string]any); ok {
			line := bpHumanLine(b)
			extra := ""
			if obj["once"] == true {
				extra = " (once)"
			}
			return fmt.Sprintf("■ breakpoint set: %s%s", line, extra)
		}
	case "remove":
		return fmt.Sprintf("■ breakpoint %v removed", obj["id"])
	case "enable":
		return fmt.Sprintf("■ breakpoint %v enabled", obj["id"])
	case "disable":
		return fmt.Sprintf("■ breakpoint %v disabled", obj["id"])
	case "clear":
		return "■ all breakpoints cleared"
	case "condition":
		return fmt.Sprintf("■ breakpoint %v condition: %q", obj["id"], obj["cond"])
	case "list":
		if items, ok := obj["items"].([]map[string]any); ok {
			var b strings.Builder
			b.WriteString("  ID  HITS  LOCATION")
			for _, it := range items {
				flags := ""
				if it["disabled"] == true {
					flags += " [disabled]"
				}
				if it["trace"] == true {
					flags += " [trace]"
				}
				line := bpHumanLine(it)
				fmt.Fprintf(&b, "\n  #%-3d %-5d %s%s", it["id"], it["totalHitCount"], line, flags)
			}
			return b.String()
		}
	}
	return fmt.Sprintf("bp %v", op)
}

func bpHumanLine(b map[string]any) string {
	loc := ""
	if f, ok := b["file"].(string); ok && f != "" {
		loc = fmt.Sprintf("%s:%v", f, b["line"])
	} else if fn, ok := b["function"].(string); ok && fn != "" {
		loc = fn
	}
	name := ""
	if n, ok := b["name"].(string); ok && n != "" {
		name = fmt.Sprintf(" %q", n)
	}
	return fmt.Sprintf("#%v%s %s", b["id"], name, loc)
}

func humanStop(obj map[string]any) string {
	reason := obj["reason"]
	loc := locOf(obj)
	var b strings.Builder
	switch reason {
	case "breakpoint":
		if bp, ok := obj["bp"].(map[string]any); ok && len(bp) > 0 {
			id := bp["id"]
			name := ""
			if n, ok := bp["name"].(string); ok && n != "" {
				name = fmt.Sprintf(" %q", n)
			}
			fmt.Fprintf(&b, "● breakpoint #%v%s hit — %s", id, name, loc)
		} else {
			fmt.Fprintf(&b, "● breakpoint hit — %s", loc)
		}
	case "step":
		fmt.Fprintf(&b, "◈ stepped — %s", loc)
	case "halted":
		fmt.Fprintf(&b, "◼ halted — %s", loc)
	default:
		fmt.Fprintf(&b, "◈ stopped — %s", loc)
	}
	if src, ok := obj["source"].([]map[string]any); ok {
		for _, s := range src {
			marker := "  "
			if s["current"] == true {
				marker = "▸ "
			}
			fmt.Fprintf(&b, "\n%s%4d %s", marker, s["n"], s["text"])
		}
	}
	return b.String()
}

func locOf(obj map[string]any) string {
	file, _ := obj["file"].(string)
	line, _ := obj["line"].(int)
	fn, _ := obj["function"].(string)
	if file == "" && fn == "" {
		return "(no location)"
	}
	loc := fmt.Sprintf("%s:%d", file, line)
	if fn != "" {
		loc += " (in " + fn + ")"
	}
	return loc
}

func humanGoroutines(obj map[string]any) string {
	items, _ := obj["items"].([]map[string]any)
	sel, _ := obj["selected"].(int64)
	var b strings.Builder
	for _, g := range items {
		marker := "  "
		if g["selected"] == true {
			marker = "◉ "
		}
		loc := ""
		if f, ok := g["file"].(string); ok && f != "" {
			loc = fmt.Sprintf(" %s:%v", f, g["line"])
			if fn, ok := g["function"].(string); ok && fn != "" {
				loc += " " + fn
			}
		}
		fmt.Fprintf(&b, "%sgoroutine %v [%v]%s\n", marker, g["id"], g["status"], loc)
	}
	if sel == 0 {
		return strings.TrimSuffix(b.String(), "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func humanStack(obj map[string]any) string {
	frames, _ := obj["frames"].([]map[string]any)
	var b strings.Builder
	for i, f := range frames {
		fmt.Fprintf(&b, "#%d %v %s:%v\n", i, f["function"], f["file"], f["line"])
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func humanVars(kind string, obj map[string]any) string {
	vars, ok := obj["vars"].([]format.VarView)
	if !ok {
		return kind + ": (no vars)"
	}
	var b strings.Builder
	for _, v := range vars {
		b.WriteString(format.OneLine(v))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func humanEval(obj map[string]any) string {
	v, ok := obj["value"].(format.VarView)
	if !ok {
		return fmt.Sprintf("%s = ?", obj["expression"])
	}
	return format.OneLine(v)
}

func humanSource(obj map[string]any) string {
	src, _ := obj["source"].([]map[string]any)
	var b strings.Builder
	for _, s := range src {
		marker := "  "
		if s["current"] == true {
			marker = "▸ "
		}
		fmt.Fprintf(&b, "%s%4d %s\n", marker, s["n"], s["text"])
	}
	return strings.TrimSuffix(b.String(), "\n")
}
