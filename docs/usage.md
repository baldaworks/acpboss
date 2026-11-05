# Use ACP and Responses HTTP

## ACP stdio

Configure your ACP client to launch:

```sh
acpboss stdio --config /absolute/path/to/config.yaml
```

Initialization starts all configured providers. Model choices are presented as
`provider/upstream-id`, including upstream IDs that themselves contain slashes.
Select through `session/set_config_option` with `configId:"model"`.

A logical ACP session keeps a separate backend session per provider. Switching
A → B → A resumes A's own history; histories are not copied between providers.
Other config options and modes belong to the selected provider. Close unused
sessions with `session/close`; there is no idle eviction.

Closing releases logical routes and requests backend `session/close` where the
provider advertises it. Other providers can retain backend state until their
process exits. HTTP releases each request's logical session after completion.

Permissions are forwarded to the owning ACP client. Authentication-method and
tool-call IDs are qualified by provider. Client filesystem/terminal callbacks
and session load/resume are unadvertised and unsupported; ACP v2 is outside the
current contract. See the [method-by-method mapping](protocol-mapping.md#acp-frontend-to-provider-acp).

## HTTP routes

Start `acpboss api --config config.yaml`. Supported routes are:

| Route | Purpose |
| --- | --- |
| `GET /v1/models` | Discover qualified model IDs. |
| `POST /v1/responses` | Generate a text response from text or supported inline media. |

The catalog is discovered at server startup and excludes dead provider
connections. Restart to rediscover changed model availability. Each generation
request creates a fresh backend session on its selected provider.

```sh
curl -sS http://127.0.0.1:8080/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"PROVIDER/UPSTREAM_MODEL_ID","input":"Hello","store":false}'
```

`store:false` must be explicit. Optional `instructions` is a string. The body
and nested content reject unknown fields; use the [OpenAPI schema](openapi.yaml)
for the accepted subset. `temperature`, `reasoning`, `max_output_tokens`, external
`tools`, stored continuation, background generation and Chat Completions are
not translated. Unsupported paths return 404; invalid fields return 400.

## Status and errors

An accepted generation normally returns HTTP 200 even if generation fails.
Inspect the Response:

- `status:"completed"`: successful end of turn.
- `status:"incomplete"`: provider stopped at its token limit.
- `status:"failed"`: inspect `error`; any collected text may still be present.

Text is in `output[0].content[0].text`; `usage` is `null`. Upstream token counts
are not translated. Setup/validation errors use HTTP error statuses with an
`error` object instead of a Response. See the [error table](protocol-mapping.md#errors-and-cancellation).

## Streaming

```sh
curl -N http://127.0.0.1:8080/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"PROVIDER/UPSTREAM_MODEL_ID","input":"Hello","store":false,"stream":true}'
```

SSE uses Responses lifecycle events, including `response.output_text.delta`
and one terminal `response.completed`, `response.incomplete` or `response.failed`.
Sequence numbers increase from zero. There is no `[DONE]` sentinel or Chat
Completions chunk format. A disconnected client cannot receive a terminal event;
its owning backend prompt is cancelled.

## Use an OpenAI client

Point a Responses-compatible client at `http://127.0.0.1:8080/v1`, use a qualified
model from the catalog, and send `store:false`. SDKs that require an API-key
argument can use a placeholder: the default acpboss listener has no API-key
authentication, and that key is not used to authenticate providers. Provider
credentials stay with the provider CLI.

The test suite uses OpenAI Go SDK v3.32.0. A Go client can use the same setup:

```go
client := responses.NewResponseService(
    option.WithBaseURL("http://127.0.0.1:8080/v1/"),
    option.WithAPIKey("local-placeholder"),
    option.WithMaxRetries(0),
)
result, err := client.New(ctx, responses.ResponseNewParams{
    Model: shared.ResponsesModel("PROVIDER/UPSTREAM_MODEL_ID"),
    Store: param.NewOpt(false),
    Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("Hello")},
})
```

Imports are `github.com/openai/openai-go/v3/responses`, `/option`,
`/packages/param`, and `/shared`. Handle `err`, then inspect `result.Status`
and `result.OutputText()`. Use `NewStreaming` for typed SSE consumption.

## Continue a conversation

Carry earlier input and assistant output into the next request:

```json
{
  "model": "PROVIDER/UPSTREAM_MODEL_ID",
  "store": false,
  "instructions": "Be concise.",
  "input": [
    {"role":"user","content":"Remember the word violet."},
    {"role":"assistant","content":"Understood."},
    {"role":"user","content":"What was the word?"}
  ]
}
```

acpboss represents roles/history as deterministic textual context. This does
not reconstruct native structured upstream conversation state. Previous
assistant `output` message items can be replayed when they contain only text
with empty annotations/logprobs. `previous_response_id` and `conversation`
must be absent or `null`.

## Tools, permissions and deployment

Providers may execute their own native tools. ACP clients receive tool updates;
HTTP does not expose client-executed functions or a permission dialog. If a
provider requests interactive permission over HTTP, the prompt is cancelled and
the Response fails. HTTP `tools:[]` output does not disable provider-native tools.

The default listener is loopback with no built-in authentication or TLS. Keep
that default for local use; if exposing it beyond your machine, put access
control and TLS at the deployment boundary. Choose a working directory and
provider permissions appropriate to the requests you intend to serve.

SIGINT/SIGTERM and ACP stdin EOF trigger shutdown. acpboss cancels active work
and reaps its owned provider processes within `shutdown_timeout`; on Unix,
cleanup includes their process groups. ACP stdout contains protocol frames only;
readiness and sanitized application diagnostics go to stderr.
