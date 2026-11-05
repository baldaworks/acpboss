# Repository guidance

## Project and boundaries

acpboss is a Go CLI with two frontends: ACP v1 stdio and an explicit stateless
OpenAI Responses HTTP subset. Both route to owned ACP subprocesses. Provider
IDs are namespaces, not vendor-specific dispatch types. Preserve first-slash
model routing, separate backend histories, cancellation and process cleanup.

Read README.md and the relevant guide under docs/ before changing behavior.
Use existing package structure; cmd/ is an entrypoint and internal/ owns the
implementation. Do not introduce orchestration frameworks or provider-specific
network clients for behavior the provider executable already owns.

## HTTP contract and generated code

Edit docs/openapi.yaml for HTTP schemas; run make generate. Never hand-edit
internal/openai/api/api.gen.go. Use generated request/response/SSE types rather
than parallel handwritten wire structs. Keep tools/oapi-codegen.yaml and the
isolated tools module reproducible. make generate-check detects stale artifacts
without modifying files.

Preserve explicit store:false, strict unknown-field rejection, correct terminal
SSE events and the distinction between HTTP status and Response status. ACP
media capability checks apply to the selected provider; never silently drop
unsupported assistant media or automatically switch providers.

## Verification

Use the local Go version, matching go.mod and tools/go.mod. Normal checks:

```sh
make check
```

For dependency changes run make tidy and make mod-verify. Pinned generator,
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
