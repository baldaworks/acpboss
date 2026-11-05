# acpboss documentation

acpboss launches configured ACP provider processes and exposes them through an
ACP stdio frontend or an OpenAI-compatible HTTP frontend. The HTTP interface
supports Responses, Chat Completions and live ACP conversations.

## Start here

1. [Get started](getting-started.md): build, authenticate a provider and send a request.
2. [Configure providers](configuration.md): launch commands, defaults, profiles and limits.
3. [Choose an interface](usage.md): ACP sessions or HTTP JSON/SSE and native conversation continuity.
4. [Send images and files](multimodality.md): supported forms and provider requirements.

## HTTP API

- [Routes and request boundaries](usage.md#http-routes)
- [Live conversations and metadata](usage.md#continue-a-conversation)
- [Streaming event behavior](usage.md#streaming)
- [Complete OpenAPI contract](openapi.yaml)

The HTTP subset includes model discovery, Responses generation, stateless Chat
Completions and process-local conversation create/get/metadata-update/delete.
It does not implement legacy Completions, stored responses or conversation
item/history endpoints.

## Reference and operations

| Document | What it answers |
| --- | --- |
| [Troubleshooting](troubleshooting.md) | Why did startup, discovery or generation fail? |
| [Protocol mapping](protocol-mapping.md) | Which fields, methods, IDs and events are translated? |
| [OpenAPI contract](openapi.yaml) | Which HTTP request/response/error shapes are accepted? |
| [Development](development.md) | How do I build, regenerate, test and validate real providers? |
| [Contributing](../CONTRIBUTING.md) | How should I prepare a change? |

Use the [README](../README.md) for the project overview. The guides describe
implemented behavior; an advertised provider model/capability is not a promise
of account access or model-specific media support.
