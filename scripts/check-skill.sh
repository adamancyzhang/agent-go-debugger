#!/bin/bash
# Lint the frontmatter of every skills/*/SKILL.md (same heuristic as
# agent-java-debugger's build.js):
#   - frontmatter block must exist (file starts with ---);
#   - exactly one `name` and one `description` key;
#   - a PLAIN scalar value may not contain ": " (breaks strict YAML
#     parsers — "mapping values are not allowed here"); quoted scalars
#     and block scalars are legal YAML and skipped.
#
# usage: bash scripts/check-skill.sh
set -u
cd "$(dirname "$0")/.." || exit 1
bad=0

for skill in skills/*/SKILL.md; do
	[ -f "$skill" ] || continue
	# block between the first two ^---$ lines
	block=$(awk '
		NR == 1 && /^---$/ { inblock = 1; next }
		inblock && /^---$/ { exit }
		inblock { print }
	' "$skill")
	if [ -z "$block" ]; then
		echo "check: $skill: no frontmatter block"
		bad=1
		continue
	fi
	name=0
	desc=0
	err=""
	while IFS= read -r line; do
		case "$line" in
			"" | \#*) continue ;;
		esac
		kv=${line%%:*}
		rest=${line#*:}
		case "$line" in
			*:*) ;;
			*) continue ;; # not a key: value line
		esac
		case "$kv" in
			[A-Za-z]*)
				value=$(printf '%s' "$rest" | sed 's/^[[:space:]]*//')
				case "$kv" in
					name) name=$((name + 1)) ;;
					description) desc=$((desc + 1)) ;;
				esac
				case "$value" in
					"" | "|" | ">") continue ;;
					\'* | \") continue ;; # quoted scalar: legal YAML
					*": "*) err="${err}    ${kv} value contains \": \" — quote it or rewrite\n" ;;
				esac
				;;
		esac
	done <<EOF
$block
EOF
	if [ "$name" -ne 1 ]; then echo "check: $skill: expected exactly one name (got $name)"; bad=1; fi
	if [ "$desc" -ne 1 ]; then echo "check: $skill: expected exactly one description (got $desc)"; bad=1; fi
	if [ -n "$err" ]; then printf "check: %s:\n%b" "$skill" "$err"; bad=1; fi
done

if [ "$bad" -eq 0 ]; then
	echo "skill frontmatter OK"
else
	exit 1
fi
