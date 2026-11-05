# Get started

## 1. Build

From a project checkout with Go 1.26.10 or newer:

```sh
go build -trimpath -o bin/acpboss ./cmd/acpboss
./bin/acpboss --help
```

`make build` produces the same binary. acpboss is an executable; its `internal`
packages are implementation details rather than a public Go library API.

## 2. Prepare a provider

Install an ACP-capable provider and authenticate it using its own CLI. acpboss
uses the provider's environment/account; it does not exchange an HTTP API key
for provider credentials.

For example, authenticate an installed OpenCode using `opencode auth login`,
or use an authenticated Codex installation through its ACP adapter. Node/npm
must be available if your launch command uses `npx`.

Create a file named `config.yaml`:

```yaml
runtime:
  providers:
    opencode:
      cmd: "opencode acp"
acpboss:
  working_dir: .
```

To add Codex, add another provider under the same `providers` map:

```yaml
    codex:
      cmd: "npx -y codex-acp@1.11.0"
```

Each configured provider must start and initialize successfully. Include only
providers you can run. The [example configuration](../examples/config.yaml)
contains both commands and explicit default settings.

## 3. Start one interface

For applications and SDKs:

```sh
./bin/acpboss api --config config.yaml
```

The default listener is `127.0.0.1:8080`. Readiness is printed to stderr. Providers
are initialized and model discovery completes before the listener starts.

For an ACP client, supply these executable arguments in that client's agent
configuration:

```text
command: /absolute/path/to/acpboss
arguments: stdio --config /absolute/path/to/config.yaml
```

This describes the executable and argv, not a universal client configuration
schema. Use the field names required by your client. The client starts acpboss
and exchanges ACP v1 messages over stdin/stdout; no HTTP server is involved.
Each serving mode runs in its own process.

## 4. Find a model and make a request

In another terminal:

```sh
curl -sS http://127.0.0.1:8080/v1/models
```

Copy a complete model `id` into this request:

```sh
curl -sS http://127.0.0.1:8080/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"PROVIDER/UPSTREAM_MODEL_ID","input":"Reply with exactly OK.","store":false}'
```

Expect `status:"completed"` and the assistant text in
`output[0].content[0].text`. `store:false` is mandatory. For SSE, add
`"stream":true` and use `curl -N`.

Model IDs split at the first slash: `opencode/openai/gpt-6.1-sol` routes to the
OpenCode process with upstream ID `openai/gpt-6.1-sol`. Free models use exactly
the same routing. Their availability changes; always discover actual IDs.

Stop the server with Ctrl-C. acpboss shuts down its owned provider processes.
Next: [usage](usage.md), [configuration](configuration.md),
[images and files](multimodality.md), or [troubleshooting](troubleshooting.md).
