package provider

import (
	"context"
	"encoding/json"
	"fmt"
)

// Model is a provider-reported model and its actual selection mechanism.
type Model struct {
	ID, Name, ConfigID string
	Legacy             bool
}

// Metadata preserves session JSON, including fields missing from the typed SDK.
type Metadata struct {
	ID            string
	Raw           map[string]any
	Models        []Model
	Current       string
	ModelConfigID string
}

// DecodeMetadata normalizes modern grouped/select or legacy model metadata.
func DecodeMetadata(raw json.RawMessage, modelConfigID string) (*Metadata, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("invalid session metadata")
	}
	m := &Metadata{Raw: obj, ModelConfigID: modelConfigID}
	m.ID, _ = obj["sessionId"].(string)
	options, _ := obj["configOptions"].([]any)
	for _, raw := range options {
		o, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := o["id"].(string)
		if (modelConfigID != "" && id != modelConfigID) || (modelConfigID == "" && o["category"] != "model") {
			continue
		}
		if o["type"] != "select" || id == "" {
			return nil, fmt.Errorf("model option is not a valid select")
		}
		m.Current, _ = o["currentValue"].(string)
		choices, _ := o["options"].([]any)
		m.Models = collectModels(choices, id)
		break
	}
	if len(m.Models) == 0 {
		if legacy, ok := obj["models"].(map[string]any); ok {
			m.Current, _ = legacy["currentModelId"].(string)
			models, _ := legacy["availableModels"].([]any)
			for _, raw := range models {
				o, _ := raw.(map[string]any)
				id, _ := o["modelId"].(string)
				name, _ := o["name"].(string)
				if id != "" {
					m.Models = append(m.Models, Model{ID: id, Name: name, Legacy: true})
				}
			}
		}
	}
	if len(m.Models) == 0 {
		return nil, fmt.Errorf("provider has no discoverable models")
	}
	seen := map[string]bool{}
	found := false
	for _, model := range m.Models {
		if model.ID == "" || seen[model.ID] {
			return nil, fmt.Errorf("invalid or duplicate model ID")
		}
		seen[model.ID] = true
		if model.ID == m.Current {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("reported current model is absent from catalog")
	}
	return m, nil
}

func collectModels(options []any, id string) []Model {
	var result []Model
	for _, raw := range options {
		o, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if nested, ok := o["options"].([]any); ok {
			result = append(result, collectModels(nested, id)...)
			continue
		}
		value, _ := o["value"].(string)
		name, _ := o["name"].(string)
		result = append(result, Model{ID: value, Name: name, ConfigID: id})
	}
	return result
}

// NewSession creates a backend session while retaining legacy response fields.
func (p *Process) NewSession(ctx context.Context, cwd string, mcp []any) (*Metadata, error) {
	if mcp == nil {
		mcp = []any{}
	}
	raw, err := p.Call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": mcp})
	if err != nil {
		return nil, err
	}
	m, err := DecodeMetadata(raw, p.Config.Defaults.ModelConfigID)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.Config.ID, err)
	}
	if m.ID == "" {
		return nil, fmt.Errorf("provider %q: empty session ID", p.Config.ID)
	}
	return m, nil
}

// SelectModel removes no namespace itself: it accepts only a reported upstream ID.
func (p *Process) SelectModel(ctx context.Context, m *Metadata, id string) (*Metadata, error) {
	var choice *Model
	for i := range m.Models {
		if m.Models[i].ID == id {
			choice = &m.Models[i]
			break
		}
	}
	if choice == nil {
		return nil, fmt.Errorf("unknown upstream model")
	}
	method := "session/set_config_option"
	params := map[string]any{"sessionId": m.ID, "configId": choice.ConfigID, "value": id}
	if choice.Legacy {
		method = "session/set_model"
		params = map[string]any{"sessionId": m.ID, "modelId": id}
	}
	raw, err := p.Call(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if choice.Legacy {
		copy := *m
		copy.Current = id
		return &copy, nil
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return nil, fmt.Errorf("invalid model selection response")
	}
	obj["sessionId"] = m.ID
	b, _ := json.Marshal(obj)
	next, err := DecodeMetadata(b, m.ModelConfigID)
	if err != nil {
		return nil, err
	}
	if next.Current != id {
		return nil, fmt.Errorf("provider did not select requested model")
	}
	return next, nil
}
