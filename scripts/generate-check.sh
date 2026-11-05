#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
"$root/scripts/generate.sh" "$tmp/api.gen.go"
if ! cmp -s "$root/internal/openai/api/api.gen.go" "$tmp/api.gen.go"; then
    echo 'Generated HTTP contract is stale; run task generate.' >&2
    exit 1
fi
