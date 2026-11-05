# Contributing to acpboss

Thanks for helping improve acpboss. For a bug, include the acpboss/provider
versions, serving mode, a minimal configuration without secrets, steps to
reproduce, expected behavior and the actual response/error. Distinguish model
discovery from generation. Keep credentials and raw private provider logs out
of reports.

For a substantial behavior change, describe the user-visible problem and the
proposed contract before implementing it. Prefer small, focused changes that
preserve existing routing and session guarantees.

## Before submitting

1. Read the [developer guide](docs/development.md) and relevant
   [protocol mapping](docs/protocol-mapping.md).
2. Add focused behavior tests for a bug or feature; normal tests must not use
   real accounts or contact model services.
3. Update the OpenAPI source and run `task generate` when changing HTTP wire
   formats. Include generated artifacts with their specification changes.
4. Update the user documentation for changed commands or behavior.
5. Run `task check`. For dependency changes, also run `task tidy` and
   `task mod-verify` in the documented workflow.

Real-provider checks are opt-in. If relevant, record the exact provider/model,
capability and outcome, including whether a feature was unsupported or actually
failed. Never label a skipped or failed case as passed.

In a pull request, explain the problem, resulting behavior, relevant checks and
material limitations. Do not hand-edit generated code or add unrelated tool,
architecture or formatting changes.

## Compatibility and project conventions

acpboss is a CLI application with an explicit Responses subset. Its `internal`
packages are not a public Go library API. Preserve documented CLI/configuration,
model namespaces, errors and protocol semantics unless the change explicitly
addresses compatibility. Do not imply full OpenAI API compatibility.

Use `gofmt`, keep dependencies small and keep provider-specific behavior inside
provider executables. See [AGENTS.md](AGENTS.md) for repository-specific guidance
used by coding agents.

Contributions are distributed under the project's [MIT License](LICENSE).
