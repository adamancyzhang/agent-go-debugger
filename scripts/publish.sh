#!/bin/bash
# Publish the npm distribution.
#
# Deploy is separate from building: run `npm run build` first to produce the
# platform packages under npm/, then publish them:
#
#   bash scripts/publish.sh [--dry-run]
#
# Order matters: every platform package must be on the registry before the
# main package (its optionalDependencies resolve at install time, so the
# order of publishes themselves is what npm checks when people install).
# Uses the version from the root package.json everywhere.
set -eu
cd "$(dirname "$0")/.." || exit 1

VERSION=$(node --input-type=commonjs -p "require('./package.json').version")
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY="--dry-run"

# Platform packages must already be built (`npm run build`). Refuse to
# publish stale or partial output instead of building here.
missing=0
for dir in npm/agent-go-debugger-*/; do
	if [ ! -x "${dir}bin/agent-go-debugger" ] && [ ! -x "${dir}bin/agent-go-debugger.exe" ]; then
		echo "missing platform binary under $dir" >&2
		missing=1
	fi
done
if [ "$missing" -ne 0 ]; then
	echo "run \`npm run build\` first to produce the platform binaries" >&2
	exit 1
fi

echo "== publishing platform packages (version $VERSION)"
for dir in npm/agent-go-debugger-*/; do
	name=$(node --input-type=commonjs -p "require('./$dir/package.json').name")
	echo "publish $name@$VERSION"
	# shellcheck disable=SC2086
	(cd "$dir" && npm publish $DRY --access public)
done

echo "== publishing main package @adamancyzhang/agent-go-debugger@$VERSION"
npm publish $DRY --access public

echo "done. verify: npm view @adamancyzhang/agent-go-debugger versions"
