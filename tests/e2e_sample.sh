#!/bin/bash
# End-to-end tests for agent-go-debugger against the sample target running
# under a dlv headless server (start it first: bash tests/run_sample.sh).
#
# Each scenario drives ONE persistent session over a fifo (like an agent
# would), asserting on the JSONL event stream.
#
# usage: AGD=/path/to/agent-go-debugger bash tests/e2e_sample.sh [dlv-port] [http-port]
set -u
cd "$(dirname "$0")" || exit 1

AGD="${AGD:-$(cd .. && pwd)/dist/agent-go-debugger}"
DLV_PORT="${1:-23456}"
HTTP_PORT="${2:-18080}"
BASE="http://127.0.0.1:${HTTP_PORT}"
WORK="$(mktemp -d /tmp/agd-e2e.XXXXXX)"
PASS=0
FAIL=0
CUR=""       # current session name
CURLOG=""    # current session log
CURFIFO=""

if [ ! -x "$AGD" ]; then
	echo "agent-go-debugger not found at $AGD — build first: go build -o dist/agent-go-debugger ." >&2
	exit 2
fi

step() { echo; echo "== $1"; }
check() {
	if [ "$2" = "0" ]; then PASS=$((PASS + 1)); echo "  ok: $1"
	else FAIL=$((FAIL + 1)); echo "  FAIL: $1"; fi
}

# start_session <name>: opens a persistent attach session reading from a fifo.
start_session() {
	CUR="$1"
	CURLOG="$WORK/$1.log"
	CURFIFO="$WORK/$1.ctl"
	rm -f "$CURLOG" "$CURFIFO"
	mkfifo "$CURFIFO"
	"$AGD" attach --port "$DLV_PORT" --json --hold --in "$CURFIFO" \
		--resume-after 0 --timeout 12 > "$CURLOG" 2>&1 &
	exec 9>"$CURFIFO"
	sleep 1
	grep -q '"type":"attach"' "$CURLOG" || sleep 1
}

# send <command text>
send() { echo "$1" >&9; }

# wait_evt <regex> [seconds] — waits for an event line in the session log.
wait_evt() {
	local re="$1" sec="${2:-12}" i
	for i in $(seq 1 $((sec * 2))); do
		grep -qE "$re" "$CURLOG" 2>/dev/null && return 0
		sleep 0.5
	done
	return 1
}

# end_session: quit and clean up
end_session() {
	send "quit"
	sleep 1.5
	exec 9>&-
	wait 2>/dev/null
	CUR=""
}

# fire <url> : curl in the background (used to hit a breakpoint while the
# session sits in continue).
fire() { (curl -s --max-time 8 "$1" -o /dev/null >/dev/null 2>&1 &); }

# --- S1: default runnable state -----------------------------------------------
step "S1 default runnable state"
curl -s --max-time 3 "$BASE/status" >/dev/null 2>&1
check "service answers while nothing is connected" "$?"

# --- S2: function breakpoint, stop, locals/args/stack, step, release ----------
step "S2 function breakpoint + inspection + step + release"
start_session s2
send "bp add --function main.(*Work).Step --name stepb"
wait_evt '"op":"add"'
check "bp add function" "$?"
send "continue"
sleep 1.5
fire "$BASE/tick?i=5"
wait_evt '"reason":"breakpoint"'
check "stop on breakpoint (bp=stepb)" "$?"
grep -q '"name":"stepb"' "$CURLOG" && check "stop names stepb" 0 || check "stop names stepb" 1
send "next"
wait_evt '"reason":"step"'
check "next lands a step stop" "$?"
send "locals"
wait_evt '"type":"locals"'
check "locals emitted after next" "$?"
grep -q '"name":"doubled"' "$CURLOG" && check "locals include doubled" 0 || check "locals include doubled" 1
send "args"
wait_evt '"type":"args"'
grep -q '"name":"i"' "$CURLOG" && check "args include i" 0 || check "args include i" 1
send "stack"
wait_evt '"type":"stack"'
grep -q 'main.(_Work)._Step\|main.(\*Work).Step' "$CURLOG" && check "stack frame 0 = Step" 0 || check "stack frame 0 = Step" 1
send "continue"
sleep 2
curl -s --max-time 3 "$BASE/status" >/dev/null 2>&1
check "service responsive again after release" "$?"
send "bp remove stepb"
wait_evt '"op":"remove"'
end_session

# --- S3: condition --------------------------------------------------------------
step "S3 condition: true stops, false never stops"
start_session s3
send "bp add --function main.(*Work).Step --name condb --cond 'i == 4'"
wait_evt '"op":"add"'
send "continue"
sleep 1.5
fire "$BASE/tick?i=3"
wait_evt '"type":"timeout"|"reason":"breakpoint"' 14
grep -q '"reason":"breakpoint"' "$CURLOG" && check "i==3 does not stop" 1 || check "i==3 does not stop" 0
sleep 1
send "continue"
sleep 1
fire "$BASE/tick?i=4"
wait_evt '"reason":"breakpoint"' 14
check "i==4 stops" "$?"
send "continue"
sleep 1.5
send "bp remove condb"
wait_evt '"op":"remove"'
end_session

# --- S4: --once -----------------------------------------------------------------
step "S4 --once removes itself after first hit"
start_session s4
send "bp add --function main.(*Work).Step --name onceb --once"
wait_evt '"op":"add"'
send "continue"
sleep 1.5
fire "$BASE/tick?i=1"
wait_evt '"reason":"breakpoint"' 14
check "once stops first time" "$?"
send "continue"
sleep 2
send "bp list"
wait_evt '"op":"list"'
tail -1 "$CURLOG" | grep -q '"op":"list"' && LIST_LINE=$(grep '"op":"list"' "$CURLOG" | tail -1)
echo "$LIST_LINE" | grep -q '"name":"onceb"' && check "once removed after hit" 1 || check "once removed after hit" 0
end_session

# --- S5: tracepoint ---------------------------------------------------------------
step "S5 tracepoint does not pause"
start_session s5
send "bp add --function main.(*Work).Step --name traceb --trace"
wait_evt '"op":"add"'
send "continue"
sleep 1.5
fire "$BASE/tick?i=7"
sleep 2
curl -s --max-time 3 "$BASE/status" >/dev/null 2>&1
check "service still answers while tracepoints fire" "$?"
send "trace"
wait_evt '"type":"trace"' 10
check "trace event emitted" "$?"
send "bp remove traceb"
wait_evt '"op":"remove"'
end_session

# --- S6: disable / enable -----------------------------------------------------------
step "S6 disable / enable"
start_session s6
send "bp add --function main.(*Work).Step --name disb"
wait_evt '"op":"add"'
send "bp disable disb"
wait_evt '"op":"disable"'
send "continue"
sleep 1.5
fire "$BASE/tick?i=2"
wait_evt '"type":"timeout"' 14
check "disabled bp does not stop" "$?"
send "bp enable disb"
wait_evt '"op":"enable"'
send "continue"
sleep 1
fire "$BASE/tick?i=2"
wait_evt '"reason":"breakpoint"' 14
check "re-enabled bp stops" "$?"
send "continue"
sleep 1.5
send "bp clear"
wait_evt '"op":"clear"'
end_session

# --- S7: goroutines / source / file breakpoint -------------------------------------
step "S7 file breakpoint + goroutines"
start_session s7
send "bp add --file $(cd sample && pwd)/main.go --line 38 --name lineb"
wait_evt '"op":"add"'
check "bp add file:line" "$?"
send "continue"
sleep 1.5
fire "$BASE/tick?i=9"
wait_evt '"reason":"breakpoint"' 14
check "file:line stop" "$?"
send "goroutines"
wait_evt '"type":"goroutines"'
N=$(grep -o '"type":"goroutines"' "$CURLOG" >/dev/null && grep -c '"id":' "$CURLOG")
[ "${N:-0}" -ge 3 ] && check "goroutines listed (>=3)" 0 || check "goroutines listed (>=3)" 1
send "source"
wait_evt '"type":"source"'
grep -q '"current":true' "$CURLOG" && check "source window present" 0 || check "source window present" 1
send "continue"
sleep 1.5
send "bp remove lineb"
end_session

# --- S8: quit keeps the target alive ------------------------------------------------
step "S8 quit keeps target alive"
curl -s --max-time 3 "$BASE/status" >/dev/null 2>&1
check "service answers after session quit" "$?"

# --- S9: exit event -------------------------------------------------------------------
step "S9 exit event (crash endpoint)"
start_session s9
send "continue"
sleep 1.5
fire "$BASE/boom?crash=1"
wait_evt '"type":"exited"' 14
check "exited event emitted" "$?"
end_session

echo
echo "==== results: $PASS passed, $FAIL failed ===="
if [ "${KEEP_WORK:-0}" = "1" ]; then echo "work kept: $WORK"; else rm -rf "$WORK"; fi
[ "$FAIL" = "0" ]
