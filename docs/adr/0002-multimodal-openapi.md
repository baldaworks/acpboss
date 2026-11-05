# Multimodal ACP and an executable OpenAPI contract

Keep ACP v1 as the provider transport. Validate all five native content variants
at the shared router boundary, retain payload/extension metadata, and check the
selected provider's advertised image/audio/embeddedContext capabilities before
prompting. Advertise their union so clients can use capable providers even when
another configured provider is text-only; do not switch providers automatically.

Use docs/openapi.yaml (OpenAPI 3.1) as the HTTP source of truth. Pin oapi-codegen
v2.8.1 in an isolated Go tools module and generate strict net/http interfaces,
routing, wire types and the embedded specification. JSON Schema 2020-12 request
validation uses this embedded contract; generated decoding alone does not enforce
all const/null/union constraints. Keep reproducibility checking in make check,
using temporary output comparisons without requiring git operations.

The HTTP adapter remains a stateless Responses subset. Inline image data URLs
map to ACP image blocks, and inline file data URLs plus filename map to opaque
embedded blob resources. Mixed messages retain ordering with text role/boundary
records; existing text-only replay stays unchanged. No remote fetcher, uploaded
file service, transcoder or new audio endpoint is introduced.

ACP forwards multimodal output. HTTP keeps its text output contract and explicitly
fails on non-text assistant output, retaining preceding text and normal terminal
SSE semantics. Inventing image-generation tool items from arbitrary ACP media
would misrepresent upstream behavior.

Handwritten code owns ACP adaptation, session lifecycle and bounded SSE framing;
it uses generated HTTP payload types rather than parallel wire structs. An
OpenAPI generator or runtime can be replaced behind this boundary. The cost is
additional generation/runtime validation dependencies; contract fixtures and
OpenAI SDK integration tests protect compatibility.
