# agent-go-debugger

Remote debugging CLI for **Go** processes, for AI agents and terminals.
The sibling of [agent-py-debugger](../agent-py-debugger) and
[agent-java-debugger](../agent-java-debugger): attach to a Go target over a
TCP debug port and drive it — breakpoints, stepping, goroutines, stack,
locals, expression evaluation — emitting machine-readable JSON events
(`--json`) for agents.

The CLI **never manages the target process**. It connects over JSON-RPC to a
Delve headless server (`dlv --headless`) that runs next to the target —
exactly like agent-java-debugger attaches to a JDWP port.

```
[target machine]
  dlv exec ./app --headless --listen=:2345 --api-version=2 --accept-multiclient --continue -- <args...>
        │ TCP JSON-RPC (:2345)
[anywhere — your machine / CI / agent]
  agent-go-debugger attach --host <target> --port 2345
```

## Install

The CLI ships as an npm package with per-platform prebuilt binaries
(no Go toolchain, no runtime dependencies — the binary for your machine is
installed automatically as an optional dependency):

```bash
npm i -g @adamancyzhang/agent-go-debugger
agent-go-debugger version
```

Supported: darwin arm64/x64, linux arm64/x64, win32 x64 (Delve has no
native backend for windows/arm64; Windows-on-ARM runs the x64 build via
emulation).

Build from source:

```bash
go build -o dist/agent-go-debugger .
```

## Starting the target side (the only "setup" on the target)

```bash
# launch the Go service under the debugger and let it serve immediately
dlv exec ./your-app --headless --listen=:2345 --api-version=2 \
  --accept-multiclient --continue -- <app args...>

# or attach the debugger to an already running process
dlv attach --headless --listen=:2345 --api-version=2 --accept-multiclient <PID>
```

Notes:

- `--continue` lets the target serve **without any debugger client
  connected** — attach later and it only stops when a breakpoint is hit.
  Without it the target stays paused at its entry point until the first
  client continues it (like `suspend=y`).
- `--accept-multiclient` keeps the server alive between clients.
- macOS needs Developer Mode once: `sudo DevToolsSecurity -enable`
  (revoke afterwards with `-disable`). Linux/Windows: nothing.
- This CLI talks to Delve v1.27+ (`--api-version=2` is the only valid value).

## Quick start

```bash
# one-shot: set a breakpoint, run, detach (target keeps serving)
agent-go-debugger attach --port 2345 \
  --exec 'bp add --function github.com/org/repo/controller.GetStatus --name gs' \
  --exec 'continue'

# interactive
agent-go-debugger attach --port 2345
```

A breakpoint hit emits a `stop` event with the full context; the target is
paused until you `continue`/`step` (see the stop-hold guard below).

```
$ agent-go-debugger attach --port 2345 --json --exec 'bp add --function ...GetStatus' --exec 'continue'
{"mode":"remote","pid":44132,"target":"127.0.0.1:2345","type":"attach"}
{"line":"bp add --function github.com/org/repo/controller.GetStatus --name gs","type":"command"}
{"bp":{"cond":"","disabled":false,"file":"/srv/app/controller/misc.go","function":"github.com/org/repo/controller.GetStatus","id":2,"line":57,"name":"gs",...},"once":false,"op":"add","type":"bp"}
{"line":"continue","type":"command"}
{"bp":{"cond":"","disabled":false,...},"file":".../misc.go","function":"...GetStatus","line":57,"reason":"breakpoint","source":[{...}],"type":"stop"}
```

## Commands

| command | meaning |
|---|---|
| `bp add --file PATH --line N` / `--function NAME` | set a breakpoint (`--cond`, `--name`, `--trace`, `--once`, `--hit-expr '>N'`, `--disabled`) |
| `bp list` `bp remove <id\|name>` `bp enable/disable <id\|name>` `bp clear` `bp condition <id\|name> EXPR` | manage breakpoints |
| `continue` `c` `next` `n` `step` `s` `fin` | execution |
| `halt` | pause a running target |
| `goroutines` `goroutine <id>` | goroutine list / switch current |
| `stack` `locals` `args` `eval EXPR` `source` | inspection |
| `status` `trace` | session state / flush tracepoint hits |
| `quit` `kill` | end session (target keeps running) / terminate (server-owned) |

Function breakpoints need the full name with its package path
(`github.com/org/repo/package.Func`, receivers as `pkg.(*T).M`); bare names
are ambiguous. File paths are matched against the target's build-time paths
(absolute paths work best).

## Exit codes

`0` all good · `1` any command failed or a wait timed out · `2` usage or
connection error · `130` interrupted.

## JSON events

`--json` emits one JSON object per line on stdout (the target's own output
is redirected to stderr so the stream stays pure). Stable event `type`s:
`attach`, `command`, `bp`, `stop`, `trace`, `timeout`, `exited`, `error`,
`goroutines`, `goroutine`, `stack`, `locals`, `args`, `eval`, `source`,
`status`, `handoff`, `quit`. See `skills/agent-go-debugger/SKILL.md`
for the full schemas.

## Design semantics (vs the py/java siblings)

- **Runnable by default.** The target serves normally; nothing pauses it
  until a breakpoint is actually hit or an execution command runs.
- **Interrupt-and-resume.** Breakpoint management and inspection briefly
  halt + resume a running target around their RPC (millisecond-scale), so a
  service is never wedged by "set a breakpoint while live".
- **Stop-hold guard.** When the target is stopped and no command arrives
  within `--resume-after` (default 60s), it is auto-resumed and an `error`
  event is emitted, so a dead agent cannot freeze a service.
- **Quit never kills.** `quit` resumes a stopped target and disconnects;
  breakpoints stay registered on the server for the next client. `kill` is
  explicit and only meaningful for server-owned (exec) targets.
- **No per-goroutine pause.** Delve pauses the whole process on a stop
  (Go has no equivalent of Python's per-thread tracing); `next`/`step`
  resume execution during the step, tracepoints never pause at all.
- **No pending breakpoints.** Go binaries carry all symbols at build time:
  a location that cannot be resolved now (dead code, inlined, wrong path)
  fails immediately with a hint.
- **No exception breakpoints.** An unhandled panic exits the process (an
  `exited` event with `reason: panic`); to observe panics set a breakpoint
  in your `recover()`/middleware instead.

## Development

```bash
npm run check-skill             # lint SKILL.md frontmatter (name/description,
                                # no bare ": " in plain scalars)
bash tests/run_sample.sh        # starts a sample Go service under dlv (port 23456)
bash tests/e2e_sample.sh        # runs the end-to-end scenarios
```

## Publishing (npm)

Deploy never builds. Each `npm publish` runs that package's own
`prepublishOnly`, which builds exactly the binary it ships
(`scripts/build.js --one <platform>`) right before uploading — a publish can
never ship stale output, and nothing is ever built twice:

```bash
npm run build                   # optional: full cross-compile matrix at once
npm run deploy                  # publishes each platform package (each one
                                # builds itself via prepublishOnly first),
                                # then the main package
npm run deploy -- --dry-run     # rehearsal without uploading
```

A single platform package can be published on its own — it still builds
itself first. Requires `npm login` beforehand.
