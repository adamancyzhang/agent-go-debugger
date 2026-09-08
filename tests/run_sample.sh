#!/bin/bash
# Starts the sample debug target under a dlv headless server (mirror of the
# recommended target-side launch, see README).
#
# usage: bash tests/run_sample.sh [dlv-port] [sample-http-port]
set -u
cd "$(dirname "$0")/sample" || exit 1

DLV_PORT="${1:-23456}"
HTTP_PORT="${2:-18080}"
DLV="$(command -v dlv 2>/dev/null || echo "$HOME/go/bin/dlv")"

if [ ! -x "$DLV" ]; then
	echo "dlv not found; install with: go install github.com/go-delve/delve/cmd/dlv@latest" >&2
	exit 1
fi

mkdir -p tmp
echo "building sample (no optimizations, line numbers deterministic)..."
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" go build -gcflags="all=-N -l" -o tmp/sample . || exit 1

echo "starting dlv headless on 127.0.0.1:${DLV_PORT} ..."
SAMPLE_PORT="$HTTP_PORT" "$DLV" exec ./tmp/sample \
	--headless --listen="127.0.0.1:${DLV_PORT}" \
	--api-version=2 --accept-multiclient --continue \
	> tmp/dlv.log 2>&1 &
echo $! > tmp/dlv.pid

# wait for the API server and the http service
for _ in $(seq 1 40); do
	if curl -s "http://127.0.0.1:${HTTP_PORT}/status" >/dev/null 2>&1; then
		echo "sample ready: dlv=127.0.0.1:${DLV_PORT} http=127.0.0.1:${HTTP_PORT} (pid $(cat tmp/dlv.pid))"
		exit 0
	fi
	sleep 0.5
done
echo "sample failed to become ready; see tests/sample/tmp/dlv.log" >&2
exit 1
