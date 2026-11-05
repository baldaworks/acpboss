# Protocol mapping

`acpboss` exposes two alternative frontends. Both communicate with configured
provider processes using ACP v1 JSON-RPC over stdio:

```text
HTTP client / OpenAI SDK
  -> acpboss api: Responses HTTP API
    -> ACP over stdio -> codex-acp / opencode acp / another ACP executable
      -> model service chosen by that executable

ACP client / acpdump
  -> acpboss stdio: ACP v1
    -> ACP over stdio -> codex-acp / opencode acp / another ACP executable
      -> model service chosen by that executable
```

The modes run in separate processes. There is no HTTP hop between `acpboss`
and its provider executables. The provider owns its network API, credentials,
model access, tools, and upstream retention. `acpboss` does not select whether
that provider calls a Responses, Completions, or vendor-specific API.

## HTTP routes

| Client request | Implementation |
| --- | --- |
| `GET /v1/models` | List qualified models discovered through ACP sessions. |
| `POST /v1/responses` | Create a fresh provider session, select a model, send an ACP prompt, collect text, release the session. |
| `POST /v1/responses` with `stream:true` | The same lifecycle with typed Responses SSE events. |
| `/v1/chat/completions` | HTTP 404, `unsupported_endpoint`. |
| `/v1/completions` | HTTP 404, `unsupported_endpoint`. |
| Other paths, including stored-response retrieval | HTTP 404, `unsupported_endpoint`. |

An OpenAI-compatible client must use its **Responses** operation. Chat
Completions requests and response chunks are not translated. The full request
and response schemas are in [openapi.yaml](openapi.yaml).

## Provider and model identifiers

The key under `runtime.providers` is the routing namespace. It does not need
to match a vendor name:

```yaml
runtime:
  providers:
    codex:
      cmd: "npx -y codex-acp@1.11.0"
    opencode:
      cmd: "opencode acp"
```

| Provider metadata | Client-visible model | Value sent back to the provider |
| --- | --- | --- |
| `codex`: `gpt-6.1-sol` | `codex/gpt-6.1-sol` | `gpt-6.1-sol` |
| `opencode`: `opencode/crouton-alpha-free` | `opencode/opencode/crouton-alpha-free` | `opencode/crouton-alpha-free` |
| `opencode`: `openai/gpt-6.1-sol` | `opencode/openai/gpt-6.1-sol` | `openai/gpt-6.1-sol` |

Routing splits at the **first slash**. All remaining slashes belong to the
upstream model ID. The doubled `opencode/opencode/` in the example is valid:
the first component selects the process; the second is part of its model ID.
These are examples, not guaranteed available models. Free models use the same
routing as other models. Obtain actual IDs from the current catalog.

Model discovery and selection use the following mapping:

| ACP metadata from provider | Discovery | Selection request to provider |
| --- | --- | --- |
| `configOptions`, `category:model`, `type:select` | Read `currentValue` and option `value`/`name`; flatten grouped options. | `session/set_config_option`, using the advertised option's `id` as `configId` and the unqualified model as `value`. |
| Configured `model_config_id` | Find that specific config option instead of identifying it by category. | Use that option's ID. |
| Legacy `models` | Read `currentModelId` and `availableModels[].modelId/name` when modern choices are absent. | `session/set_model` with unqualified `modelId`. |

`GET /v1/models` returns `id:<provider>/<upstream>`, `owned_by:<provider>`,
`object:model`, and `created:0`, sorted by qualified ID. HTTP discovers this
catalog at startup using temporary sessions; it is not a live vendor catalog
fetch on each request. Dead provider connections are excluded. Restart the
process to rediscover changed availability. Discovery does not prove that the
account can generate with every advertised model.

## HTTP request to ACP calls

For each valid `POST /v1/responses`, `acpboss` performs:

1. Validate the HTTP body and locate the qualified model in the catalog.
2. Send `session/new` to the selected provider with the configured absolute
   `working_dir` as `cwd` and `mcpServers:[]`.
3. Apply configured provider defaults, then select the HTTP request's model
   using the modern or legacy method above.
4. Send `session/prompt` using the provider's session ID.
5. Collect `session/update` notifications and the prompt's final `stopReason`.
6. Remove the logical session and call upstream `session/close` if advertised.

Provider processes are reused across HTTP requests. Provider sessions are
fresh for each request. Providers that do not implement `session/close` can
retain backend session state until their process exits.

| HTTP request field | ACP mapping or validation |
| --- | --- |
| `model` | Select the process and upstream model as described above. |
| `input` as a nonempty string, without `instructions` | One prompt block: `{"type":"text","text":"<input>"}`. |
| `input` as a text-only message array | Normalize roles and text, serialize a transcript, send it as one ACP text block. See below. |
| `input` as a mixed-media message array | Preserve ordered text/media blocks with textual role/message boundaries. See the multimodal section below. |
| `instructions` | Include in the serialized transcript. There is no separate upstream system-message RPC. Must be a string when supplied. |
| `stream` | Choose JSON or SSE on the frontend. The backend still uses ACP prompt/update messages. |
| `store` | Must be explicitly `false`. No upstream storage setting is changed. |
| `previous_response_id`, `conversation` | Must be absent or `null`; no session lookup or continuation is performed. |
| `background` | Must be absent or `false`. |
| `text` | If supplied, only `{"format":{"type":"text"}}` is supported. |
| Other fields | HTTP 400, including `tools`, `tool_choice`, `temperature`, `max_output_tokens`, and `reasoning`. No translation is attempted. |

For message arrays, accepted roles are `user`, `assistant`, `system`, and
`developer`. `content` can be a string or a nonempty array of text parts.
`input_text` is accepted for these roles; inline `input_image` and `input_file`
are also accepted. `output_text` is accepted only for
assistant messages. Text parts are concatenated without inserted separators.
Supplied `annotations` and `logprobs` must be empty arrays. Message `id` and
valid `status` fields are accepted for replay but do not become upstream IDs
or state.

For example, this HTTP body:

```json
{
  "model": "opencode/opencode/crouton-alpha-free",
  "input": [
    {"role":"user","content":"Hello"},
    {"role":"assistant","content":"Hi"},
    {"role":"user","content":"Continue"}
  ],
  "instructions": "Be brief",
  "store": false
}
```

produces a single ACP text block whose `text` is:

```text
Continue the following conversation as the assistant. Preserve the supplied role order and use the current instructions.
{"instructions":"Be brief","messages":[{"role":"user","text":"Hello"},{"role":"assistant","text":"Hi"},{"role":"user","text":"Continue"}]}
```

A string input with `instructions` uses the same transcript format with one
`user` message. This is textual conversation replay: roles are encoded in the
text, rather than reconstructed as native structured provider history. To
continue a conversation over HTTP, include earlier input and assistant output
in the next request's `input` array.

## ACP output to HTTP response

| Provider output | HTTP representation |
| --- | --- |
| `session/update` with `sessionUpdate:agent_message_chunk` and `content.type:text` | Append `content.text` to `output[0].content[0].text`; emit `response.output_text.delta` when streaming. |
| Thought chunks, tool updates, config updates | No HTTP output text or tool items. |
| Non-text `agent_message_chunk` | Explicit failed Response / `response.failed`; preceding text is retained. |
| Successful prompt result with `stopReason:end_turn` | Response `status:completed`; assistant message `status:completed`. |
| Successful prompt result with `stopReason:max_tokens` | Response `status:incomplete`, `incomplete_details.reason:max_output_tokens`; assistant message `status:incomplete`. |
| Any other or missing `stopReason` | Response `status:failed`, `error.code:server_error`. |
| Prompt RPC error, timeout, output limit, permission interaction, or update queue overflow | Response `status:failed`, `error.code:server_error`; collected partial text is retained. |
| Provider token usage | Not translated: HTTP `usage:null`, even if the provider reports usage. |

The generated `resp_…` and `msg_…` IDs belong to the HTTP response; they are
not provider session IDs. The final output contains one assistant `message`
with one `output_text` content part, with empty `annotations` and `logprobs`.
`completed_at` is set only for `completed` responses. HTTP metadata reports
`store:false`, `previous_response_id:null`, `tools:[]`, `tool_choice:none`,
and `parallel_tool_calls:false`.

Provider-native tools can still execute within the provider's own permissions.
The HTTP `tools:[]` output does not disable them. If a provider requests client
permission, the HTTP adapter replies with a cancelled outcome, cancels the
prompt, and reports failure. HTTP has no permission dialog or client-executed
function-call loop.

With `stream:true`, the event order on a writable connection is:

```text
response.created
response.in_progress
response.output_item.added
response.content_part.added
response.output_text.delta       (zero or more nonempty text chunks)
response.output_text.done
response.content_part.done
response.output_item.done
response.completed | response.incomplete | response.failed
```

Each SSE frame has matching `event:` and JSON `type` fields and a monotonically
increasing `sequence_number`, starting at zero. There is one terminal event;
there is no `[DONE]` sentinel. A disconnected client cannot receive terminal
events; its prompt is cancelled.

## Errors and cancellation

Errors before generation use an HTTP error object with `error.message`,
`error.type`, `error.param`, and `error.code`:

| Condition | HTTP status |
| --- | --- |
| Invalid JSON, unsupported field, content, or state | 400 |
| Unknown/unavailable model or unsupported path | 404 |
| Non-POST method on `/v1/responses` | 405 with `Allow: POST` |
| Request body exceeds the configured limit | 413 |
| Request/session capacity exhausted | 429 |
| Provider session/model setup failure | 502 |
| Provider setup reports authentication required (`-32000`) | 503 |
| Provider setup deadline expires | 504 |
| SSE requested on a transport without flushing support | 500 |

After setup, generation failure normally returns HTTP **200** with
`status:failed`, or the SSE `response.failed` event. Check `status` and `error`
instead of treating HTTP 200 alone as successful generation.

Provider RPC error codes are retained, but their messages are sanitized:
`-32000` becomes `authentication required`, `-32601` becomes `method unsupported`,
`-32602` becomes `invalid upstream parameters`, and other errors become
`upstream RPC failed`. This message alone does not identify the original cause.
Provider stderr and raw SDK diagnostics are discarded. Reproduce directly with
the provider CLI when investigating the upstream error.

Client disconnect, request timeout, cancellation, or a slow/failed output sink
causes `session/cancel` to be sent to the owning provider session. The session
remains busy until the prompt returns or its bounded RPC deadline expires.
After an unacknowledged RPC timeout, that binding cannot be reused.

## ACP frontend to provider ACP

The stdio frontend preserves persistent logical sessions and forwards a broader
set of ACP updates than the HTTP frontend:

| ACP client operation | Provider mapping |
| --- | --- |
| `initialize` | Start and initialize all configured providers; return protocol version 1, `acpboss` identity, session-close capability, aggregated auth methods, and the union of optional prompt capabilities across live providers. |
| `authenticate` with `<provider>/<method>` | Call only that provider's `authenticate` with the unqualified `methodId`. |
| `session/new` | Create a backend session in every live configured provider; forward the client's absolute `cwd` and `mcpServers`. Return a new logical `sessionId`. |
| `session/set_config_option`, `configId:model` | Split the qualified model, select its owning provider and model, return an aggregate snapshot. |
| Other `session/set_config_option` or `session/set_mode` | Forward to the currently selected provider, replacing the logical `sessionId` with its backend ID. |
| `session/prompt` | Validate text, resource links, image, audio and embedded resources; check selected-provider capabilities; forward original blocks with the backend session ID. |
| `session/cancel` | Cancel the session's active prompt on its owning backend. |
| `session/close` | Remove logical routes and close each backend session where supported. |
| Other methods, including session load/resume and frontend `session/set_model` | Method not found. Use the aggregate model config option to select a model. |

The initial provider is the first live configured provider in sorted ID order.
Each logical session retains a separate backend session per provider.
Switching A → B → A resumes A's history; B does not receive A's conversation.

The returned `configOptions` contains an aggregate `id:model` selector with
qualified choices, followed by the selected provider's non-model controls.
Legacy `modes` also come from the selected provider. Model IDs are qualified;
non-model config IDs and mode IDs remain provider-local.

For provider callbacks and notifications:

| Provider field or callback | Client-facing mapping |
| --- | --- |
| Backend `sessionId` | Replace with the owning logical session ID. |
| `current_model_update.modelId` / `currentModelId` | Prefix with `<provider>/`. |
| `toolCallId` on updates or permission `toolCall` | Prefix with `<provider>/`. |
| Config-option update from the selected provider | Refresh its metadata and emit the aggregate model selector plus that provider's controls. |
| `session/request_permission` during a prompt | Forward to the owning ACP client; return its outcome to the provider. |
| Filesystem/terminal callbacks | Unsupported and unadvertised. |

ACP prompt results retain provider result fields, including `stopReason` and
any usage metadata. The HTTP adapter performs the narrower translation above.

## Configuration to provider behavior

| Provider configuration | Effect |
| --- | --- |
| `cmd`, `extra_args` | Parse the command into argv and append extra arguments; execute directly without a shell. |
| `inherit_env`, `env` | Build the subprocess environment; inheritance defaults to enabled, explicit keys override inherited values. |
| `{{.Model}}` inside command arguments | Expand once from the configured `model` at process launch; HTTP model selection does not relaunch the process. |
| `model` | Select this upstream model when creating each backend session; an HTTP request subsequently selects its own requested model. |
| `model_config_id` | Identify the provider's model config option for discovery and selection. |
| `mode` | Select the advertised mode config option, or use legacy `session/set_mode` if there is no such config option. |
| `reasoning_effort`, `reasoning_effort_config_id` | Select a `thought_level` config option or the explicit config ID. Config values are limited to `minimal`, `low`, `medium`, `high`, `xhigh`, and must also be accepted by the provider. |
| `system_instructions` | Prepend a separate ACP text block to every prompt; this applies to both frontends. |

## Checking a real provider

First inspect the provider directly, then inspect the aggregate frontend:

```sh
acpdump --json -- opencode acp
acpdump --json -- ./bin/acpboss stdio --config examples/config.yaml
```

`acpdump` checks initialization and session metadata. It does not send a
generation prompt. To test generation through the HTTP mapping:

```sh
./bin/acpboss api --config examples/config.yaml
```

In another terminal, list models and substitute an actual ID into the request:

```sh
curl -sS http://127.0.0.1:8080/v1/models
curl -sS http://127.0.0.1:8080/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"PROVIDER/UPSTREAM_MODEL_ID","input":"Reply with exactly ACPBOSS_OK. Do not use tools.","store":false}'
```

Successful generation has `status:completed` and the requested text in
`output[0].content[0].text`. Free-model availability can change; use a model
advertised by the running provider and inspect failures separately from
successful catalog discovery.


## Multimodal content

The stdio frontend accepts every ACP v1 content variant. Optional feature
advertisement is the union across live providers at initialization; the
selected provider is checked before each prompt. An image-capable provider
does not imply that every model/account accepts images. No provider fallback
or media conversion is automatic.

| ACP block | Required provider prompt capability | Handling |
| --- | --- | --- |
| `text` | Baseline | String text, metadata preserved. |
| `resource_link` | Baseline | Required name/URI; no resource fetching by acpboss. |
| `image` | `image:true` | Base64 data and image MIME type; optional source URI and metadata preserved. |
| `audio` | `audio:true` | Base64 data and audio MIME type; metadata preserved. |
| `resource` | `embeddedContext:true` | Resource URI plus exactly one text/blob value; opaque content preserved. |

Malformed media or a missing selected-provider capability fails before upstream
`session/prompt`: ACP returns `-32602`; HTTP returns 400 with a content location
in `error.param`. Source bytes are not included in error messages. Frame limits
include the configured instruction prefix and the serialized ACP RPC envelope.

| HTTP part | ACP mapping | Accepted source |
| --- | --- | --- |
| `input_text`, assistant `output_text` replay | `text` | String text; supplied annotations/logprobs must be empty arrays. |
| `input_image` | `image` with MIME/base64 extracted from data URL | `image_url:data:image/<subtype>;base64,...`; detail absent or `auto`. |
| `input_file` | `resource` with `mimeType`, base64 `blob` and an inert generated attachment URI | `file_data:data:<mime>;base64,...` and nonempty `filename`. |

For example, `input_file` at message 0, part 3, with `filename:note.pdf` becomes:

```json
{
  "type": "resource",
  "resource": {
    "uri": "acpboss-attachment:///message/0/part/3/note.pdf",
    "mimeType": "application/pdf",
    "blob": "BASE64_BYTES"
  }
}
```

Filename components are URI-escaped. This URI identifies an attachment; it is
never fetched. Files remain opaque: acpboss does not extract PDF text, render
pages, transcode audio or guarantee the provider can interpret a MIME type.
Remote image URLs, `file_url`, uploaded `file_id`, non-auto image detail and HTTP
audio parts are rejected explicitly. Native ACP audio remains available.

Text-only normalization above is unchanged. For a mixed-media conversation, the
ACP prompt has the following ordered layout:

```text
configured system_instructions text block, if any
text block: conversation framing and JSON containing current instructions
text block: {"message":0,"role":"user","boundary":"begin"}
original text/media blocks of message 0 in their original order
text block: {"message":0,"role":"user","boundary":"end"}
text block: {"message":1,"role":"assistant","boundary":"begin"}
original text/media blocks of message 1 in their original order
text block: {"message":1,"role":"assistant","boundary":"end"}
...remaining messages...
```

These are textual context records, not native structured roles or a privilege
boundary. Media bytes are separate ACP blocks, not text inside the transcript.
ACP output updates retain original media/metadata and normal routing ID rewrites.
HTTP still exposes text output: a non-text assistant content block terminates
with an explicit failed Response or one `response.failed` terminal event,
retaining text collected before it. Tool/thought updates retain their existing
HTTP filtering. No synthetic image-generation or audio tool objects are created.

## Generated HTTP contract

Edit [openapi.yaml](openapi.yaml), then run `make generate` from the repository
root. The pinned generator config is `tools/oapi-codegen.yaml`; tooling is
isolated in `tools/go.mod`. Generated models, strict server interfaces, routing
and the embedded spec live in `internal/openai/api/api.gen.go`. Request validation
uses JSON Schema 2020-12 compiled from that embedded spec. HTTP handlers and SSE
writers consume the generated types.

`make generate-check` generates into a temporary directory and byte-compares
without overwriting artifacts. It is part of `make check`, including CI. Commit
specification and generated artifacts together when preparing an authorized
commit; never edit generated files directly.

For a real image-input smoke check, create a known image locally,
use a current image-capable model from `/v1/models`, and send `input_text` plus
`input_image` as shown in the README. Ask a question that depends on the pixels,
not merely an `OK` response. Test direct provider ACP and acpboss separately;
use acpdump for initialization/capabilities only. Free OpenCode models follow
the same mapping, but their image support and availability are model-specific.
