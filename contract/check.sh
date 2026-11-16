#!/bin/sh
# Checks the API of a daemon against the contract of this MCP: the
# operations of contract/routes.txt, as contract/om3-api.yaml describes them,
# compared with the API of opensvc/om3 at REF, a branch or a release tag.
# Fails on a change oasdiff calls breaking for a client of one of those
# operations, method and path: an operation gone, a parameter required, a
# response of another shape. A change of another method of a path in use is
# not one of them.
#
#   contract/check.sh [REF]                  REF defaults to main
#   CONTRACT_TARGET=file contract/check.sh   checks a local api.yaml instead
set -eu

ref=${1:-main}
dir=$(dirname "$0")
target=${CONTRACT_TARGET:-}
if [ -z "$target" ]; then
	target=$(mktemp)
	trap 'rm -f "$target"' EXIT
	curl -fsSL --retry 3 -o "$target" "https://raw.githubusercontent.com/opensvc/om3/$ref/daemon/api/api.yaml"
fi

echo "contract: $(sed -n 's/^  version: //p' "$dir/om3-api.yaml" | head -1) at $(cat "$dir/om3-api.ref"), against $(sed -n 's/^  version: //p' "$target" | head -1) at ${CONTRACT_TARGET:-$ref}"
# The paths the MCP uses, as one pattern, their braces and dots literal: the
# operations of other paths are left out of the comparison.
paths=$(cut -d' ' -f2 "$dir/routes.txt" | sort -u | sed 's/[][\.*^$(){}?+|]/\\&/g' | paste -sd'|' -)
# Pinned: a release of oasdiff changes what it reports.
changes=$(go run github.com/oasdiff/oasdiff@v1.35.0 breaking "$dir/om3-api.yaml" "$target" \
	--match-path "^($paths)\$" --format json)
# The errors on the operations in use, method and path.
broken=$(printf '%s' "$changes" | jq -c --rawfile routes "$dir/routes.txt" '
	($routes | split("\n") | map(select(length > 0))) as $used
	| [(. // [])[] | select(.level >= 3) | select((.operation + " " + .path) as $op | $used | index($op))]')
if [ "$(printf '%s' "$broken" | jq length)" -eq 0 ]; then
	echo "no breaking change on the operations this MCP uses"
	exit 0
fi
printf '%s' "$broken" | jq -r '.[] | "\(.operation) \(.path): [\(.id)] \(.text)"'
exit 1
