// Package proxy routes qualified models and isolated logical sessions.
package proxy

import (
	"fmt"
	"strings"

	"github.com/baldaworks/acpboss/internal/provider"
)

// Model identifies a model externally and within its owning provider.
type Model struct{ ID, Name, ProviderID, UpstreamID string }

// AvailableModels excludes models as soon as their process or connection exits.
func AvailableModels(p *provider.Process, m *provider.Metadata) []Model {
	if !p.Alive() {
		return nil
	}
	return QualifyModels(p.Config.ID, m)
}

// QualifyModels preserves the full upstream ID, including embedded slashes.
func QualifyModels(id string, m *provider.Metadata) []Model {
	models := make([]Model, 0, len(m.Models))
	for _, v := range m.Models {
		models = append(models, Model{ID: id + "/" + v.ID, Name: v.Name, ProviderID: id, UpstreamID: v.ID})
	}
	return models
}

// SplitModel accepts only an explicitly provider-qualified model identifier.
func SplitModel(id string) (string, string, error) {
	p, up, ok := strings.Cut(id, "/")
	if !ok || p == "" || up == "" {
		return "", "", fmt.Errorf("model must be provider/model")
	}
	return p, up, nil
}
