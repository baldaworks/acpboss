# Development

Use Go **1.26.10+**, the version in both `go.mod` files. Runtime code lives in
the root module; generation/lint/audit tools are pinned in the isolated `tools`
module. Install [Task](https://taskfile.dev/docs/installation) v3.44.0 to run
the commands in `Taskfile.yml` (CI uses the same version). Python 3 and real
provider accounts are needed only for opt-in smoke checks. Normal tests use
local deterministic ACP subprocesses and a dummy key.

## Build and check

```sh
task build          # bin/acpboss
task test           # all package and binary integration tests
task race           # race detector
task vet
task lint           # pinned golangci-lint via go tool
task audit          # pinned govulncheck via go tool
task mod-verify     # verify downloaded modules in both modules
task check          # generation drift, format, tests, race, vet, lint, audit, build
```

`task check` is the local verification entrypoint. CI separates tests/build/audit
and lint into two workflows, using `go-version-file: go.mod` and the same Task
commands. Real-account calls are not part of CI.

After changing dependencies:

```sh
task tidy
task mod-verify
```

Use the locally available Go version when deliberately updating module Go
versions. Keep dependency changes scoped and update the corresponding sum file.
The pinned Go tool command pattern is:

```sh
go tool -modfile=tools/go.mod golangci-lint run ./...
go tool -modfile=tools/go.mod govulncheck ./...
```

## Change the HTTP API

[openapi.yaml](openapi.yaml) is the source of truth for HTTP routes, requests,
responses, errors and SSE payloads. It describes explicit Responses and Chat
Completions subsets, not the entire upstream OpenAI API.

```sh
task generate
task generate-check
```

`go generate ./internal/openai/api` is the equivalent generation entrypoint.
The generator version/config are in `tools/go.mod` and `tools/oapi-codegen.yaml`.
Generated `internal/openai/api/api.gen.go` contains wire types, the strict
`net/http` server interface, routing and embedded specification. Do not edit it
by hand. Change source schemas and regenerate in the same change.

The handwritten adapter owns ACP translation, session lifecycle and bounded SSE
framing. It uses generated wire types. Request validation compiles JSON Schema
2020-12 from the embedded contract, including `const`, nullable unions and strict
nested fields. Do not introduce duplicate handwritten HTTP schema structs.

`task generate-check` generates into a temporary directory and compares bytes;
it fails on drift without changing repository files. When updating a tool,
review generated output and run SDK/contract tests as well as `task check`.
Chat Completions tests cover raw JSON/SSE and the official OpenAI Go SDK for
both non-streaming and streaming calls.

## Repository map

| Path | Responsibility |
| --- | --- |
| `cmd/acpboss` | Executable entrypoint |
| `internal/cli`, `internal/config` | CLI and validated configuration |
| `internal/acp`, `internal/openai` | Frontends and HTTP-to-ACP adaptation |
| `internal/proxy` | Qualified routing, sessions, content validation and cancellation |
| `internal/provider`, `internal/protocol` | Owned processes and bounded ACP JSON-RPC |
| `internal/testprovider` | Deterministic subprocess fixtures |
| `docs`, `examples` | User/reference guides, contract and example configuration |
| `scripts`, `tools` | Reproducible generation and verification |

## Real-provider smoke checks

Install [acpdump](https://github.com/baldaworks/acpdump), authenticate providers
and make sure their commands can run.
This command sends real account requests and prints a temporary artifacts path:

```sh
task build
python3 scripts/smoke-real.py \
  --binary bin/acpboss --acpdump /path/to/acpdump \
  --provider 'codex=npx -y codex-acp@1.11.0' \
  --provider 'opencode=opencode acp'
```

The script uses advertised current models unless overridden with, for example,
`--model 'opencode=UPSTREAM_MODEL_ID'`. Choose a currently available free model
when testing the free tier. Provider-wide image capability does not guarantee
support in every selected model.

It runs acpdump on direct and aggregate ACP endpoints, then verifies text and a
known image through direct ACP, acpboss ACP and HTTP. It checks actual answers,
records JSON results and stops owned processes. For each provider it also creates
a live HTTP conversation, sends a fresh random token, then asks for it in a
second request containing no token or replayed messages. Exact recall, completed
status, conversation reference, store:false and deletion/404 are checked. Unsupported advertised modality
absence is recorded separately; an attempted generation failure fails the run.
No tools or repository access are needed for the requested model answers.
Review raw provider diagnostic artifacts locally before sharing them.

For discovery only:

```sh
acpdump --json -- opencode acp
acpdump --json -- ./bin/acpboss stdio --config examples/config.yaml
```

acpdump initializes a session and inspects metadata; it does not test generation.
