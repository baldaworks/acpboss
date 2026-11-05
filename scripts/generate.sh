#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output=${1:-"$root/internal/openai/api/api.gen.go"}
cd "$root/tools"
go tool oapi-codegen --config oapi-codegen.yaml -o "$output" ../docs/openapi.yaml
