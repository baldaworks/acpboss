# Images, audio and files

Provider-wide capabilities are checked before prompting. A capability describes
what the executable can route; individual models/accounts may still reject a
format. acpboss never switches your selected provider to satisfy a modality.

| Content | ACP stdio input/output | Responses HTTP input | Chat Completions input | HTTP output |
| --- | --- | --- | --- | --- |
| Text | Yes | Yes | Yes | Yes |
| Images | With provider support | Inline image data URL | Inline user `image_url` data URL | Explicit failure on assistant image content |
| Audio | With provider support | Unsupported | Unsupported | Unsupported |
| Embedded files/resources | With provider support | Inline file data URL plus filename | Unsupported | Explicit failure on assistant resource content |
| Resource links | Yes | Unsupported as a message part | Unsupported | Explicit failure on assistant link content |

ACP initialization advertises the union of optional capabilities across live
providers. Each prompt is then checked against the selected provider. Missing
capability or malformed content returns ACP `-32602`, or HTTP 400 before
`session/prompt` is sent. Payloads and extension metadata are preserved on ACP.

## Send an image over HTTP

Base64-encode a local image and include it as a data URL. For example, with
Python 3 installed, replace the model and file path:

```sh
python3 - <<'PY' > /tmp/acpboss-image-request.json
import base64, json, pathlib
image = base64.b64encode(pathlib.Path("image.png").read_bytes()).decode()
print(json.dumps({
    "model": "PROVIDER/UPSTREAM_MODEL_ID",
    "store": False,
    "input": [{"role": "user", "content": [
        {"type": "input_text", "text": "Describe what is in this image."},
        {"type": "input_image", "image_url": "data:image/png;base64," + image,
         "detail": "auto"}
    ]}]
}))
PY
curl -sS http://127.0.0.1:8080/v1/responses \
  -H 'Content-Type: application/json' \
  --data-binary @/tmp/acpboss-image-request.json
```

The selected provider needs `image:true`. Match the MIME type to the actual
bytes. Only absent/`auto` detail is supported: ACP has no equivalent selector
for `low`, `high` or `original`. Remote URLs and uploaded `file_id` values are
rejected; acpboss does not fetch image content.

For Chat Completions, put the same data URL in a user content part:

```json
{"type":"image_url","image_url":{"url":"data:image/png;base64,BASE64_BYTES","detail":"auto"}}
```

Chat image parts are user-only. Text parts use `{"type":"text","text":"…"}`.

## Send a file over HTTP

Use the same request structure with this content part:

```json
{
  "type": "input_file",
  "filename": "note.pdf",
  "file_data": "data:application/pdf;base64,BASE64_BYTES"
}
```

Replace `BASE64_BYTES` with the file's base64 content. The selected provider
needs `embeddedContext:true`. acpboss creates an opaque embedded blob resource
with a deterministic, escaped attachment URI. It does not extract PDF text,
render pages or guarantee that a particular provider can interpret the format.
`file_url` and uploaded `file_id` are unsupported.

## Ordering, output and limits

Mixed messages keep their original text/media order. Text boundary records
associate each message with its role and instructions; actual media remains in
separate ACP blocks. Configured `system_instructions` is prepended separately.
See the [exact block mapping](protocol-mapping.md#multimodal-content).

HTTP returns text. If a provider emits a non-text assistant block, acpboss returns
`status:"failed"` or SSE `response.failed`, preserving earlier text. Use the ACP
frontend to receive native multimodal output and tool content.

`max_message_bytes` includes encoded payloads, generated context and instruction
prefixes. Base64 increases payload size; large files may exceed the default
8 MiB frame/body limit before their decoded size does. Invalid base64/MIME and
unsupported capabilities fail explicitly. Provider-native limits still apply.
