// Package openai implements Responses over native ACP sessions.
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
	Conversation  string
	SourceRoot    string
	Stream        bool
	Instructions  *string
	Blocks        []any
	Sources       []string
}
type contentMessage struct {
	path    string
	role    string
	blocks  []any
	sources []string
}

func normalizeResponse(req api.CreateResponse) (normalized, error) {
	n := normalized{Model: req.Model, SourceRoot: "input", Instructions: req.Instructions, Stream: req.Stream != nil && *req.Stream}
	if req.Conversation != nil {
		id, err := req.Conversation.AsConversationSelector0()
		if err != nil {
			ref, refErr := req.Conversation.AsConversationReference()
			if refErr != nil {
				return n, invalid("conversation", "expected conversation ID or reference")
			}
			id = ref.Id
		}
		n.Conversation = id
	}
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
			cm := contentMessage{path: path, role: string(m.Role)}
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
	return finishNormalized(n, direct, messages, structured, hasMedia)
}

func normalizeChatCompletion(req api.CreateChatCompletion) (normalized, error) {
	n := normalized{Model: req.Model, SourceRoot: "messages", Stream: req.Stream != nil && *req.Stream}
	messages := make([]textMessage, 0, len(req.Messages))
	structured := make([]contentMessage, 0, len(req.Messages))
	hasMedia := false
	direct := ""
	for i, message := range req.Messages {
		path := fmt.Sprintf("messages[%d]", i)
		cm := contentMessage{path: path, role: string(message.Role)}
		content, err := message.Content.AsChatMessageContent0()
		if err == nil {
			cm.blocks = []any{map[string]any{"type": "text", "text": content}}
			cm.sources = []string{path + ".content"}
			if len(req.Messages) == 1 && message.Role == api.ChatMessageRoleUser {
				direct = content
			}
		} else {
			parts, partsErr := message.Content.AsChatMessageContent1()
			if partsErr != nil {
				return n, invalid(path+".content", "expected content string or parts")
			}
			var text strings.Builder
			for j, part := range parts {
				source := fmt.Sprintf("%s.content[%d]", path, j)
				kind, _ := part.Discriminator()
				var block map[string]any
				switch kind {
				case "text":
					value, _ := part.AsChatTextPart()
					text.WriteString(value.Text)
					block = map[string]any{"type": "text", "text": value.Text}
				case "image_url":
					if message.Role != api.ChatMessageRoleUser {
						return n, invalid(source, "image_url requires user role")
					}
					value, _ := part.AsChatImagePart()
					mediaType, data, imageErr := inlineData(value.ImageUrl.Url)
					if imageErr != nil || !strings.HasPrefix(mediaType, "image/") {
						return n, invalid(source+".image_url.url", "expected a valid base64 image data URL")
					}
					block = map[string]any{"type": "image", "mimeType": mediaType, "data": data}
					hasMedia = true
				default:
					return n, invalid(source, "unsupported content type")
				}
				cm.blocks = append(cm.blocks, block)
				cm.sources = append(cm.sources, source)
			}
			content = text.String()
		}
		messages = append(messages, textMessage{Role: string(message.Role), Text: content})
		structured = append(structured, cm)
	}
	return finishNormalized(n, direct, messages, structured, hasMedia)
}

func finishNormalized(n normalized, direct string, messages []textMessage, structured []contentMessage, hasMedia bool) (normalized, error) {
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
			n.Sources = append(n.Sources, m.path)
			n.Blocks = append(n.Blocks, m.blocks...)
			n.Sources = append(n.Sources, m.sources...)
			n.Blocks = append(n.Blocks, boundary("end"))
			n.Sources = append(n.Sources, m.path)
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
