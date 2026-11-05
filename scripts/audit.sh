#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
exec go tool -modfile="$root/tools/go.mod" govulncheck ./...
