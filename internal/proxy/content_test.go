package proxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/baldaworks/acpboss/internal/testprovider"
)

func TestMultimodalRoutingAndRejection(t *testing.T) {
	c := testprovider.Config(t, map[string]string{"alpha": "", "beta": "media"})
	capture := filepath.Join(t.TempDir(), "prompt.json")
	p := c.Runtime.Providers["beta"]
	p.Env["FIXTURE_PROMPTFILE"] = capture
	c.Runtime.Providers["beta"] = p
	r := New(c)
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	caps := r.PromptCapabilities()
	if !caps.Image || !caps.Audio || !caps.EmbeddedContext {
		t.Fatal(caps)
	}
	id, err := r.Create(t.Context(), c.Settings.WorkingDir, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	blocks := []any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "image", "data": "YQ==", "mimeType": "image/png", "_meta": map[string]any{"opaque": "keep"}},
		map[string]any{"type": "audio", "data": "Yg==", "mimeType": "audio/wav"},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///a.txt", "text": "context"}},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///b.pdf", "blob": "Yw==", "mimeType": "application/pdf"}},
		map[string]any{"type": "resource_link", "name": "a", "uri": "file:///a"},
	}
	var content *ContentError
	if _, err := r.Prompt(t.Context(), id, blocks, nil); !errors.As(err, &content) {
		t.Fatalf("unsupported prompt accepted: %v", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("rejected prompt reached media provider")
	}
	if err := r.Select(t.Context(), id, "beta/org/model"); err != nil {
		t.Fatal(err)
	}
	var received []any
	_, err = r.Prompt(t.Context(), id, blocks, func(note map[string]any) error {
		if note["sessionId"] != id {
			t.Error("session identity leaked")
		}
		update := note["update"].(map[string]any)
		if update["toolCallId"] != "beta/media-tool" {
			t.Error("tool identity not qualified")
		}
		received = append(received, update["content"])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, blocks) {
		t.Fatalf("changed media: %#v", received)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var upstream map[string]any
	if err := json.Unmarshal(raw, &upstream); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(upstream["prompt"], blocks) {
		t.Fatal("upstream payload changed")
	}
	for _, bad := range []map[string]any{
		{"type": "image", "mimeType": "image/png", "data": "not-base64"},
		{"type": "audio", "mimeType": "image/png", "data": "YQ=="},
		{"type": "resource", "resource": map[string]any{"uri": "file:///a", "text": "a", "blob": "YQ=="}},
		{"type": "resource_link", "uri": "file:///a"},
		{"type": "unknown"},
	} {
		if err := r.ValidatePrompt(id, []any{bad}); !errors.As(err, &content) {
			t.Fatalf("invalid content accepted: %#v %v", bad, err)
		}
	}
	r.Config.Settings.MaxMessageBytes = 1024
	if err := r.ValidatePrompt(id, TextPrompt(string(make([]byte, 1024)))); !errors.As(err, &content) {
		t.Fatalf("oversize prompt: %v", err)
	}
}
