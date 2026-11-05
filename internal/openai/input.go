// Package openai implements the explicitly stateless Responses subset.
package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"strings"

	"github.com/baldaworks/acpboss/internal/openai/api"
)

type inputError struct{ param, message string }

func (e *inputError) Error() string       { return e.message }
func invalid(param, message string) error { return &inputError{param, message} }

type textMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type normalized struct {
	Model, Prompt string
	Stream        bool
	Instructions  *string
	Blocks        []any
	Sources       []string
}
type contentMessage struct {
	role    string
	blocks  []any
	sources []string
}

func normalize(req api.CreateResponse) (normalized, error) {
	n := normalized{Model: req.Model, Instructions: req.Instructions, Stream: req.Stream != nil && *req.Stream}
	direct, err := req.Input.AsCreateResponseInput0()
	var messages []textMessage
	var structured []contentMessage
	hasMedia := false
	if err == nil {
		messages = []textMessage{{Role: "user", Text: direct}}
	} else {
		items, err := req.Input.AsCreateResponseInput1()
		if err != nil {
			return n, invalid("input", "expected messages")
		}
		for i, m := range items {
			path := fmt.Sprintf("input[%d]", i)
			cm := contentMessage{role: string(m.Role)}
			content, err := m.Content.AsMessageContent0()
			if err != nil {
				parts, err := m.Content.AsMessageContent1()
				if err != nil {
					return n, invalid(path+".content", "expected content parts")
				}
				var text strings.Builder
				for j, p := range parts {
					source := fmt.Sprintf("%s.content[%d]", path, j)
					kind, _ := p.Discriminator()
					var block map[string]any
					switch kind {
					case "input_text":
						v, _ := p.AsInputText()
						text.WriteString(v.Text)
						block = map[string]any{"type": "text", "text": v.Text}
					case "output_text":
						if m.Role != api.MessageRoleAssistant {
							return n, invalid(source, "output_text requires assistant role")
						}
						v, _ := p.AsOutputText()
						text.WriteString(v.Text)
						block = map[string]any{"type": "text", "text": v.Text}
					case "input_image":
						v, _ := p.AsInputImage()
						mt, data, err := inlineData(v.ImageUrl)
						if err != nil || !strings.HasPrefix(mt, "image/") {
							return n, invalid(source+".image_url", "expected a valid base64 image data URL")
						}
						block = map[string]any{"type": "image", "mimeType": mt, "data": data}
						hasMedia = true
					case "input_file":
						v, _ := p.AsInputFile()
						mt, data, err := inlineData(v.FileData)
						if err != nil {
							return n, invalid(source+".file_data", "expected a valid base64 file data URL")
						}
						uri := fmt.Sprintf("acpboss-attachment:///message/%d/part/%d/%s", i, j, url.PathEscape(v.Filename))
						block = map[string]any{"type": "resource", "resource": map[string]any{"uri": uri, "mimeType": mt, "blob": data}}
						hasMedia = true
					default:
						return n, invalid(source, "unsupported content type")
					}
					cm.blocks = append(cm.blocks, block)
					cm.sources = append(cm.sources, source)
				}
				content = text.String()
			} else {
				cm.blocks = []any{map[string]any{"type": "text", "text": content}}
				cm.sources = []string{path + ".content"}
			}
			messages = append(messages, textMessage{Role: string(m.Role), Text: content})
			structured = append(structured, cm)
		}
	}
	if hasMedia {
		// ACP prompts have no role-bearing message array. Explicit JSON boundary
		// records associate the original, ordered media/text blocks with each role.
		intro := struct {
			Instructions *string `json:"instructions,omitempty"`
		}{n.Instructions}
		raw, _ := json.Marshal(intro)
		n.Blocks = append(n.Blocks, map[string]any{"type": "text", "text": "Continue the following conversation as the assistant. Preserve the supplied message boundaries and roles.\n" + string(raw)})
		n.Sources = append(n.Sources, "instructions")
		for i, m := range structured {
			boundary := func(state string) any {
				record := struct {
					Message  int    `json:"message"`
					Role     string `json:"role"`
					Boundary string `json:"boundary"`
				}{i, m.role, state}
				raw, _ := json.Marshal(record)
				return map[string]any{"type": "text", "text": string(raw)}
			}
			n.Blocks = append(n.Blocks, boundary("begin"))
			n.Sources = append(n.Sources, fmt.Sprintf("input[%d]", i))
			n.Blocks = append(n.Blocks, m.blocks...)
			n.Sources = append(n.Sources, m.sources...)
			n.Blocks = append(n.Blocks, boundary("end"))
			n.Sources = append(n.Sources, fmt.Sprintf("input[%d]", i))
		}
		return n, nil
	}
	if direct != "" && n.Instructions == nil {
		n.Prompt = direct
		return n, nil
	}
	transcript := struct {
		Instructions *string       `json:"instructions,omitempty"`
		Messages     []textMessage `json:"messages"`
	}{n.Instructions, messages}
	encoded, _ := json.Marshal(transcript)
	n.Prompt = "Continue the following conversation as the assistant. Preserve the supplied role order and use the current instructions.\n" + string(encoded)
	return n, nil
}

func inlineData(value string) (string, string, error) {
	header, data, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") || data == "" {
		return "", "", fmt.Errorf("invalid inline data")
	}
	mt := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	media, params, err := mime.ParseMediaType(mt)
	if err != nil || len(params) != 0 {
		return "", "", fmt.Errorf("invalid MIME type")
	}
	if _, err := base64.StdEncoding.Strict().DecodeString(data); err != nil {
		return "", "", fmt.Errorf("invalid base64 data")
	}
	return media, data, nil
}
