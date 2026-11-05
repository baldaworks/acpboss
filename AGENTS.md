# Repository guidance

## Project and boundaries

acpboss is a Go CLI with two frontends: ACP v1 stdio and an explicit
OpenAI Responses HTTP subset with process-local conversation handles. Both route to owned ACP subprocesses. Provider
IDs are namespaces, not vendor-specific dispatch types. Preserve first-slash
model routing, separate backend histories, cancellation and process cleanup.

Read README.md and the relevant guide under docs/ before changing behavior.
Use existing package structure; cmd/ is an entrypoint and internal/ owns the
implementation. Do not introduce orchestration frameworks or provider-specific
network clients for behavior the provider executable already owns.

## HTTP contract and generated code

Edit docs/openapi.yaml for HTTP schemas; run task generate. Never hand-edit
internal/openai/api/api.gen.go. Use generated request/response/SSE types rather
than parallel handwritten wire structs. Keep tools/oapi-codegen.yaml and the
isolated tools module reproducible. task generate-check detects stale artifacts
without modifying files.

Keep response store:false (request store may be omitted), strict unknown-field rejection, correct terminal
SSE events and the distinction between HTTP status and Response status. ACP
media capability checks apply to the selected provider; never silently drop
unsupported assistant media or automatically switch providers.

Retain only live handle/session routing metadata and user-supplied metadata.
Providers own history; do not archive or replay bodies, create a database,
recover handles after restart or silently recreate failed native sessions.
Reserve selection and prompting together for attached HTTP turns.

## Verification

Use the local Go version, matching go.mod and tools/go.mod. Normal checks:

```sh
task check
```

For dependency changes run task tidy and task mod-verify. Pinned generator,
linter and audit dependencies live in tools/go.mod; wrappers use go tool.
Use meaningful tests of behavior and failure paths, preferably through existing
ACP subprocess fixtures and OpenAI SDK integration coverage. Normal tests must
not use account credentials or call real model services.

Real-provider scripts are opt-in; execute only within the user's authorization.
Record actual model IDs and outcomes and distinguish unsupported/skipped from
successful checks. Never expose credentials or raw private provider diagnostics.

## Working practices

Preserve unrelated user changes. Do not commit, push, publish or contact other
people without explicit authorization. If a Beads workspace is available, run
bd prime and follow its current repository/session workflow; keep task state
there instead of creating ad-hoc planning files.

Use gofmt and existing lint rules. Keep changes focused and update user docs
when behavior changes. Runtime limits, permission handling and safe error
messages are part of the contract, not incidental implementation details.
