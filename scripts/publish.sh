#!/bin/bash
# Publish the npm distribution.
#
#   bash scripts/publish.sh [--dry-run]
#
# Order matters: every platform package must be on the registry before the
# main package (its optionalDependencies resolve at install time, so the
# order of publishes themselves is what npm checks when people install).
# Uses the version from the root package.json everywhere.
set -eu
cd "$(dirname "$0")/.." || exit 1

VERSION=$(node -p "require('./package.json').version")
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY="--dry-run"

echo "== building all targets (version $VERSION)"
node scripts/build.js

echo "== publishing platform packages"
for dir in npm/@adamancyzhang/*/; do
	name=$(node -p "require('./$dir/package.json').name")
	echo "publish $name@$VERSION"
	# shellcheck disable=SC2086
	(cd "$dir" && npm publish $DRY --access public)
done

echo "== publishing main package @adamancyzhang/agent-go-debugger@$VERSION"
npm publish $DRY --access public

echo "done. verify: npm view @adamancyzhang/agent-go-debugger versions"
