# DESIGN

agent-go-debugger mirrors agent-py-debugger / agent-java-debugger for Go.

## Architecture

```
internal/cli      top-level subcommands (attach/version/help), SIGINT handling
internal/console  command runner (--exec scripts, REPL, --hold persistent
                  mode), JSONL + human rendering, stop-hold guard, exit codes
internal/delvez   the ONLY package importing Delve. Thin session layer over
                  Delve's service.Client (JSON-RPC to a headless server):
                  control (continue/next/step with generation-tagged stop
                  routing), breakpoint management, inspection, lifecycle
internal/format   display projection of values/locations (JSON + human)
```

The CLI is a **pure TCP client** (`service/rpc2.NewClient`); it never
launches, attaches or signals a process. The target side runs
`dlv exec|attach --headless --api-version=2 --accept-multiclient [--continue]`.

## Delve API baseline (pinned to github.com/go-delve/delve v1.27.1)

Verified via `go doc` (see go.mod for the exact version):

| concern | API |
|---|---|
| dial | `rpc2.NewClient(addr)` (net/rpc JSON over TCP) |
| state | `Client.GetStateNonBlocking()` — `api.DebuggerState{Running, Exited, StopReason, CurrentThread, SelectedGoroutine}` |
| continue | `Client.Continue() <-chan *api.DebuggerState` (async, one stop per call) |
| step | `Client.Next/Step/StepOut() (*api.DebuggerState, error)` (synchronous) |
| halt | `Client.Halt() (*api.DebuggerState, error)` (can interrupt a running target) |
| breakpoints | `CreateBreakpoint(*api.Breakpoint)`, `ListBreakpoints(all)`, `ClearBreakpoint(id)`, `ToggleBreakpoint(id)` (native enable/disable), `AmendBreakpoint` (conditions), `GetBreakpoint(ByName)` |
| goroutine | `SwitchGoroutine(id)`, `ListGoroutines(start, count)` |
| inspect | `Stacktrace(goid, depth, 0, opts, &cfg)`, `ListLocalVariables(scope, cfg)`, `ListFunctionArgs`, `EvalVariable(scope, expr, cfg)` |
| tracepoints | `RPCClient.GetBufferedTracepoints(cfg)` (buffered hits) |
| detach/disconnect | `Detach(kill)` — tears the server down; `Disconnect(cont)` — closes the connection only |

**Two hard-learned server behaviours (v1.27.1):**

1. **Almost every RPC blocks while the target is running.** The server only
   answers "steady-state" calls (GetVersion, GetStateNonBlocking) and the
   execution commands (Continue/Halt) while the target executes; everything
   else (breakpoints, stack, variables, ListFunctions, …) waits for the
   target to stop — forever, if nothing ever stops it. Consequence: all such
   calls run inside `Interrupt` (halt → operate → resume), never bare.
2. **Detach ends the whole server and kills an exec target.** A client that
   calls `Detach` causes the dlv server to stop and terminate the process it
   launched. Session teardown must therefore use `Disconnect` — with an
   explicit resume + poll first, because `Disconnect(cont=true)` races (it
   fires a continue and closes the connection; the server interrupts the
   in-flight continue on disconnect). `Session.Quit()` implements the safe
   sequence: resume if stopped → poll until `Running` → `Disconnect(false)`.
3. **Timeout discipline.** A timed-out `RunToStop` halts the target again
   before returning — an abandoned continue would otherwise hold the
   server's debugger lock until the next breakpoint, blocking all later RPCs.

## Stop routing (generation tags)

All execution commands run on their own goroutine and post their resulting
stop to a channel wrapped with the generation current when the command was
issued. Every intentional halt raises the generation; a stop posted under an
older generation is a continue we interrupted ourselves and is dropped — a
breakpoint is only ever reported once, by the continue that hit it. The
console loop is the single reader (poll for async hits + block on
RunToStop).

## Runnable-by-default semantics

The target starts serving immediately (`dlv --continue` on the server side).
`Connect` auto-continues a target that sits at its entry point (a server
started without `--continue`) but leaves a target stopped at a
breakpoint/step untouched, so inspection from a second client sees the real
stop context. Execution resumes only on an explicit `continue`.

## Breakpoints

- Function locations need full names (`github.com/org/repo/pkg.(*T).M`);
  failures carry a hint.
- File locations match the target's build-time source paths (absolute paths
  recommended; relative ones are resolved against the server's working dir).
- `--once` and `--trace` are client-side: a hit removes/continues as
  appropriate. Tracepoint hits never pause the target and are surfaced as
  `trace` events (buffered server-side, drained by the `trace` command and
  automatically after each trace-hit stop).
- Condition expressions are evaluated by Delve in the target (pure
  expressions only; no arbitrary calls).
- No pending/lazy breakpoints: Go binaries are complete at build time, so a
  location that cannot be resolved fails immediately (dead-code/inlining
  hint).

## Capability comparison with the py/java siblings

| | py | java | go |
|---|---|---|---|
| target requirement | injected listener | `-agentlib:jdwp` | `dlv --headless` (must be installed on the target) |
| CLI | short one-shot commands | attach session | attach session |
| events | one JSON per command | JSONL typed events | JSONL typed events |
| per-thread pause | yes | yes (suspend thread) | **no** — whole process pauses on a stop |
| exception/panic breakpoints | (exceptions via trace) | yes (JDWP exception events) | **no** — panics exit the process |
| pending breakpoints | n/a (in-process match) | yes (CLASS_PREPARE) | **no** — compile-time symbols only |
| breakpoint condition | eval in process | JVM eval | Delve DWARF eval |
| kill/detach semantics | detach keeps listener | quit detaches, VM runs | quit = disconnect (server + bps stay); kill only via explicit server control |

## Testing

`tests/sample` is a deterministic Go HTTP target (built with
`-gcflags=all=-N -l` so line numbers are stable). `run_sample.sh` starts it
under dlv headless; `e2e_sample.sh` drives the CLI through one persistent
session per scenario and asserts on the JSONL stream. A real-world scenario
for the novafare-api-vip (new-api) service is documented in
`skills/agent-go-debugger/references/novafare-e2e.md`.

Known caveat: on macOS, dlv started from a backgrounded shell can leave the
headless server in a state where the first client's breakpoint RPC never
completes; restarting the server (foreground terminal) restores full
stability. The e2e suite is best run against a server started from a real
terminal.
