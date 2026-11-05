package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// ContentError describes a local content rejection without including payloads.
type ContentError struct {
	Index    int
	Provider string
	Reason   string
}

func (e *ContentError) Error() string {
	return fmt.Sprintf("provider %q: prompt[%d]: %s", e.Provider, e.Index, e.Reason)
}

// PromptCapabilities reports optional content routable through live providers.
// Each prompt is also checked against its selected provider's capabilities.
func (r *Router) PromptCapabilities() acp.PromptCapabilities {
	var result acp.PromptCapabilities
	for _, p := range r.processes() {
		if !p.Alive() {
			continue
		}
		c := p.Init.AgentCapabilities.PromptCapabilities
		result.Image = result.Image || c.Image
		result.Audio = result.Audio || c.Audio
		result.EmbeddedContext = result.EmbeddedContext || c.EmbeddedContext
	}
	return result
}

// ValidatePrompt performs a preflight for HTTP before generation is accepted.
// Prompt repeats the check while holding the session lock.
func (r *Router) ValidatePrompt(id string, blocks []any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	if s == nil {
		return fmt.Errorf("unknown session")
	}
	b := s.bindings[s.selected]
	if b == nil || b.unusable || !b.process.Alive() {
		return ErrUnknownModel
	}
	return r.validateContent(b, blocks)
}

func (r *Router) validateContent(b *binding, blocks []any) error {
	caps := b.process.Init.AgentCapabilities.PromptCapabilities
	provider := b.process.Config.ID
	for i, raw := range blocks {
		block, ok := raw.(map[string]any)
		fail := func(reason string) error { return &ContentError{Index: i, Provider: provider, Reason: reason} }
		if !ok {
			return fail("expected a content object")
		}
		encoded, err := json.Marshal(block)
		var typed acp.ContentBlock
		if err != nil || json.Unmarshal(encoded, &typed) != nil {
			return fail("invalid content payload")
		}
		kind, _ := block["type"].(string)
		switch kind {
		case "text":
			if _, ok := block["text"].(string); !ok {
				return fail("text must be a string")
			}
		case "resource_link":
			if !nonempty(block, "name") || !validURI(block["uri"]) {
				return fail("resource link requires name and URI")
			}
		case "image", "audio":
			if (kind == "image" && !caps.Image) || (kind == "audio" && !caps.Audio) {
				return fail(kind + " content is unsupported")
			}
			mt, _ := block["mimeType"].(string)
			media, _, err := mime.ParseMediaType(mt)
			if err != nil || !strings.HasPrefix(media, kind+"/") {
				return fail("invalid media MIME type")
			}
			if !validBase64(block["data"], r.Config.Settings.MaxMessageBytes) {
				return fail("invalid or oversized base64 media")
			}
			if uri, exists := block["uri"]; exists && !validURI(uri) {
				return fail("invalid media URI")
			}
		case "resource":
			if !caps.EmbeddedContext {
				return fail("embedded context is unsupported")
			}
			resource, ok := block["resource"].(map[string]any)
			if !ok || !validURI(resource["uri"]) {
				return fail("embedded resource requires a URI")
			}
			text, hasText := resource["text"]
			blob, hasBlob := resource["blob"]
			if hasText == hasBlob {
				return fail("resource requires exactly one of text or blob")
			}
			if hasText {
				if _, ok := text.(string); !ok {
					return fail("resource text must be a string")
				}
			}
			if hasBlob && !validBase64(blob, r.Config.Settings.MaxMessageBytes) {
				return fail("invalid or oversized resource blob")
			}
			if mt, exists := resource["mimeType"]; exists {
				value, ok := mt.(string)
				if !ok {
					return fail("invalid resource MIME type")
				}
				if _, _, err := mime.ParseMediaType(value); err != nil {
					return fail("invalid resource MIME type")
				}
			}
		default:
			return fail("unknown content type")
		}
	}
	// Include the actual RPC envelope and configured instruction prefix in the
	// bound, so framing cannot fail after a prompt is marked active.
	final := blocks
	if instruction := b.process.Config.Instructions; instruction != "" {
		final = append([]any{map[string]any{"type": "text", "text": instruction}}, blocks...)
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": int64(9223372036854775807), "method": "session/prompt", "params": map[string]any{"sessionId": b.id, "prompt": final}})
	if err != nil || len(raw)+1 > r.Config.Settings.MaxMessageBytes {
		return &ContentError{Index: 0, Provider: provider, Reason: "prompt exceeds message limit"}
	}
	return nil
}
func nonempty(m map[string]any, key string) bool { v, ok := m[key].(string); return ok && v != "" }
func validURI(value any) bool {
	v, ok := value.(string)
	if !ok || v == "" {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme != ""
}
func validBase64(value any, limit int) bool {
	s, ok := value.(string)
	if !ok || s == "" || len(s) > limit || base64.StdEncoding.DecodedLen(len(s)) > limit {
		return false
	}
	_, err := base64.StdEncoding.Strict().DecodeString(s)
	return err == nil
}
