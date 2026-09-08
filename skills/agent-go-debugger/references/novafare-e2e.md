# novafare-api-vip (new-api) real-world debug recipe

Validated end-to-end against a local new-api build
(module `github.com/QuantumNous/new-api`, Gin on `:3000`).

## Target side

```bash
cd <novafare-api-vip checkout>
# stop any plain instance first, then start under the debugger
~/go/bin/dlv exec ./new-api --headless --listen=127.0.0.1:2345 \
  --api-version=2 --accept-multiclient --continue -- --port 3000
```

Verify the service serves before attaching:

```bash
curl -s http://127.0.0.1:3000/api/status        # expect 200 JSON
```

## Session

```bash
agent-go-debugger attach --port 2345 --json --timeout 30
```

```text
# function breakpoint on the status handler (full name incl. package path)
bp add --function github.com/QuantumNous/new-api/controller.GetStatus --name gs
# dlv places it at the function entry (controller/misc.go:57)

continue
# from another terminal:
#   curl http://127.0.0.1:3000/api/status
# → stop event: reason=breakpoint bp.name=gs line=57 function=...GetStatus
#   with a source window around the hit

args                 # gin.Context c etc. (locals are empty at the entry line)
eval c.Request.URL.Path   # e.g. "/api/status"
stack                # handler → middleware chain frames
next                 # step to the first statement inside the handler
locals               # now shows handler locals
continue             # release the request — the curl completes

# management round-trip while the service serves:
bp disable gs        # hit does not stop anymore (curl again to confirm)
bp enable gs
bp remove gs         # cleanup
quit                 # target keeps serving; dlv + breakpoints stay up
```

## Gotchas seen in the field

- **A breakpoint left set pauses every matching request.** If the service
  "stops responding" after a debug session, an unattended breakpoint is
  likely holding requests — attach and `continue`/`bp remove`.
- Function names must be full (`github.com/QuantumNous/new-api/controller.
  GetStatus`); short names fail with a hint.
- `quit` resumes the target then disconnects; `Detach` (any client) ends
  the whole dlv server and kills the exec target — the CLI never uses it on
  session end.
- The service binary must match the source you breakpoint against (it was
  built from `.../novafare-api-vip`, the path recorded in DWARF).
