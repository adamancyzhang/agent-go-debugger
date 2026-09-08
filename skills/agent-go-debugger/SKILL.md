---
name: agent-go-debugger
description: 'Go debugging CLI for AI agents. Attach to a Go process through its Delve headless debug port and debug it from the terminal or from agent scripts: breakpoints, stepping, goroutines, stack frames, locals, and in-memory expression evaluation. Use when the user asks to debug a Go service, set a breakpoint, trace why a request behaves unexpectedly, inspect variables at a stop, verify a code path with a real request, or debug anything running under a dlv headless server. Triggers include requests to "set a breakpoint at ...", "step through this function", "is this method ever called", "where does this request go", "inspect this variable at the breakpoint". Emits machine-readable JSON events (--json). Controls only via the debug port — never touches the target process directly.'
allowed-tools: Bash(agent-go-debugger:*)
---

# agent-go-debugger

Remote debugging CLI for Go targets. The CLI connects over TCP (JSON-RPC)
to a **Delve headless server** (`dlv --headless`) running next to the Go
service; it never launches or signals the target process itself. Attach,
manage breakpoints, hit them with real requests, inspect the stop — step,
look at locals, evaluate expressions, list goroutines.

Sibling of agent-py-debugger / agent-java-debugger (same event model:
`--json` JSONL, exit codes 0 ok / 1 command failures / 2 usage / 130
interrupt). Requires the target to run under a dlv headless server; the CLI
binary itself is self-contained.

## 1. Target-side setup (do this once per service)

```bash
dlv exec ./your-service --headless --listen=:2345 --api-version=2 \
  --accept-multiclient --continue -- <args...>
```

- `--continue` = the service starts serving immediately **without any debug
  client attached** (omit it and the process waits at its entry point for
  the first client, like `suspend=y`).
- `--accept-multiclient` = the server survives between clients and keeps
  breakpoints.
- If the service is already running without dlv: start it under dlv
  (`dlv exec`) — a debugger must launch the process; there is no Go
  equivalent of attaching mid-run without developer privileges.
- macOS: enable once with `sudo DevToolsSecurity -enable` (disable after
  with `-disable`). Linux/Windows need nothing.
- Delve must be installed on the target machine
  (`go install github.com/go-delve/delve/cmd/dlv@latest`).

## 2. Core workflow — the debug loop

```bash
# 1) attach (target keeps serving while connected)
agent-go-debugger attach --host HOST --port 2345 --json

# 2) set a breakpoint — function form is most robust:
#    use the FULL name with package path
bp add --function github.com/org/repo/controller.GetStatus --name gs

# 3) drive a real request against the service (from another shell):
#    curl http://127.0.0.1:3000/api/status
#    → the session emits {"type":"stop","reason":"breakpoint","bp":{...},"line":N,...}

# 4) inspect the stop
stack          # frames of the current goroutine
locals         # local variables of the innermost frame
args
eval c.Request.URL.Path    # evaluate an expression in the stop frame
goroutines     # all goroutines (◉ = current)
source         # source window around the stop

# 5) step or release
next           # step over
step           # step into
continue       # release the request; the service resumes serving

# 6) leave the session (target keeps running, breakpoints stay on the server)
quit
```

One-shot (agent style):

```bash
agent-go-debugger attach --host 10.0.0.5 --port 2345 --json \
  --exec 'bp add --function github.com/org/repo/controller.GetStatus --name gs' \
  --exec 'continue' --timeout 60
```

Persistent (keep a debug session alive while driving requests from
elsewhere): run with `--hold --in <fifo>`; send commands to the fifo, watch
JSONL on stdout.

## 3. Breakpoints

```bash
bp add --file path/to/file.go --line 57 --name x        # file:line
bp add --function github.com/org/repo/pkg.GetStatus     # function (full name!)
bp add --function github.com/org/repo/pkg.(*Worker).Run --cond 'n > 3'  # condition
bp add --function ... --trace                           # log hits, never pause
bp add --function ... --once                            # auto-remove after first hit
bp list / bp remove gs / bp disable gs / bp enable gs / bp clear
bp condition gs 'n == 4'                                # update/clear condition
```

Notes:

- File paths match the **build-time** paths in the binary — absolute paths
  are safest.
- Function breakpoints need the full package path; a short name that fails
  returns a hint.
- A breakpoint stops the **whole process** (Go semantics — no per-goroutine
  pause). Keep stops short and continue quickly; the stop-hold guard
  auto-resumes after `--resume-after` (default 60 s, `--resume-after 0`
  disables).
- Tracepoints (`--trace`) never pause: hits surface as `trace` events while
  the service keeps serving — use them to observe live traffic.
- `--once` removes itself at the first hit.
- Conditions are pure expressions evaluated by Delve in the target; no
  arbitrary calls.

## 4. Agent-mode conventions

- Always `--json`. One JSON object per line on stdout; the target's own
  output goes to stderr, so grep the stdout stream safely.
- Exit code 1 means a command failed or a wait timed out — check the
  `error`/`timeout` event lines.
- `continue` waits for a stop up to `--timeout`; on timeout the target is
  halted and a `{"type":"timeout"}` event emitted — re-sync with a
  `status` or another command afterwards.
- If the service stops being reachable while a breakpoint exists, it is
  probably paused at the breakpoint waiting for a client — attach,
  inspect, then `continue` (or check `status`).
- `quit` resumes a stopped target before disconnecting (a stopped service
  is never left frozen). The headless server and its breakpoints survive
  for the next session.
- `kill` terminates the target and is only offered for server-owned
  (exec) targets — not for shared servers.

## 5. Limits (Go / Delve realities)

- No exception breakpoints: an unhandled panic exits the process
  (`exited` event with `reason:"panic"`). To catch panics, set a breakpoint
  in a `defer recover()` / middleware instead.
- No pending breakpoints: unresolvable locations fail immediately (dead
  code, wrong path, inlined functions — rebuild with `-gcflags=all=-N -l`
  if inlining hides your line).
- Expression evaluation is Delve's DWARF evaluator: variables, fields,
  indexing, casts — not arbitrary Go calls.

## 6. Tests

- `tests/` — a self-contained sample Go service plus e2e scripts
  (`bash tests/run_sample.sh`, `bash tests/e2e_sample.sh`) that exercise
  the full workflow against a local dlv headless server.
