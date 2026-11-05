# Troubleshooting

## Configuration cannot be found

Pass `--config /absolute/path/to/config.yaml`. The default
`.config/acpboss/config.yaml` is relative to the process's current directory,
not your home directory. Relative `working_dir` also resolves from the CLI's
current directory. Validate environment expansion and the selected profile.
See [configuration](configuration.md).

## Provider startup or authentication fails

Run the configured command directly in the intended working directory, using
the same PATH and environment. For `npx`, ensure Node/npm and the pinned adapter
can run. Authenticate with the provider's own CLI before HTTP operation.

All configured providers must initialize successfully; startup rolls back owned
processes if one fails. Remove providers you do not intend to run from your
config. `inherit_env:false` removes inherited credentials/environment from the
child, though executable lookup still uses acpboss's PATH.

Provider stderr and raw SDK diagnostics are intentionally discarded during
normal operation because they can contain credentials. Diagnose the provider
directly; do not interpret `upstream RPC failed` as a specific root cause.

## The endpoint returns 404

Use `POST /v1/responses`, not `/v1/chat/completions` or `/v1/completions`.
The supported routes include model discovery, Responses and conversation
create/get/metadata-update/delete. Conversation items/history and stored
response retrieval are unsupported. A Responses SDK
should use base URL `http://127.0.0.1:8080/v1` and its Responses operation.
A 404 during model setup can also mean the model is unknown/unavailable.
`conversation_not_found` means the handle was deleted, belongs to another
serving process or was lost after restart.

## A model is missing or rejected

Copy the complete ID from `/v1/models`. Routing splits only the first slash:
`opencode/opencode/MODEL_ID` can be correct. Free-model IDs and account access
can change. HTTP discovers its catalog at startup; restart to rediscover it.
A listed model is not proof of generation access, and a provider-wide image
capability does not promise vision support in every model.

## HTTP 200, but no successful answer

Inspect `status`, `error` and `incomplete_details`. Generation errors are failed
Response objects; a provider token limit produces `incomplete`. Collected text
may be retained. With streaming, inspect the terminal `response.completed`,
`response.incomplete` or `response.failed` event. There is no `[DONE]` sentinel.

Interactive permission requests over HTTP are cancelled, causing failure. Use
an ACP client to handle permission requests interactively. Other provider-native
tool settings remain under the provider's control.

## A request or media attachment returns 400/413

Send `Content-Type: application/json`; omit `store` or use `store:false`. Unknown fields
are rejected; generation controls outside the documented subset are not passed
through. Check `error.param` for a location.

Use inline base64 data URLs for images/files, a filename for files, and absent
or `auto` image detail. Remote URLs, uploaded file IDs and HTTP audio parts are
unsupported. Selected providers need `image` or `embeddedContext` as appropriate.
HTTP cannot represent native assistant media output and reports an explicit
failed Response instead of omitting it.

413 means the body limit was exceeded. Frame size also includes base64,
instructions and role framing; a request within the HTTP body bound can still
fail the ACP frame preflight. See [multimodality](multimodality.md).

## Capacity, timeout or disconnected client

Close unused ACP sessions and DELETE unused HTTP conversations; there is no idle eviction. `max_sessions` and
`max_in_flight` reject excess work instead of queueing indefinitely. Adjust
limits deliberately in [configuration](configuration.md).

409 means that a turn or native RPC is still active on this conversation.
Wait for it to drain before the next turn. Different conversations are isolated.

Client disconnect, request timeout and a slow output consumer cancel the owning
backend prompt. A binding stays busy until the provider returns or its bounded
RPC deadline expires; an unacknowledged timeout makes that binding unusable.
No native binding is automatically recreated with empty context. There is no
automatic provider-process restart. Other sessions/providers stay
isolated; restart the serving process to establish fresh provider processes.

## Separate discovery from generation

Use [acpdump](development.md#real-provider-smoke-checks) to inspect initialization,
capabilities and model metadata. Then send a real generation request. The opt-in
smoke script compares direct providers, acpboss ACP and HTTP using the same
model and known input. This distinguishes successful discovery from successful
model access without assuming where an error originated.
