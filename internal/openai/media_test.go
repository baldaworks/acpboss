package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/acpboss/internal/proxy"
	"github.com/baldaworks/acpboss/internal/testprovider"
)

func mediaFixture(t *testing.T, mode string) (*httptest.Server, *Handler, map[string]string) {
	t.Helper()
	c := testprovider.Config(t, map[string]string{"alpha": mode, "beta": ""})
	paths := map[string]string{}
	for name, p := range c.Runtime.Providers {
		paths[name] = filepath.Join(t.TempDir(), name+".json")
		p.Env["FIXTURE_PROMPTFILE"] = paths[name]
		if name == "alpha" {
			p.SystemInstructions = "configured instruction"
		}
		c.Runtime.Providers[name] = p
	}
	r := proxy.New(c)
	if err := r.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := New(r)
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return server, h, paths
}
func imagePart() map[string]any {
	return map[string]any{"type": "input_image", "image_url": "data:image/png;base64,YQ=="}
}
func mediaRequest(model string, parts ...any) map[string]any {
	return map[string]any{"model": model, "store": false, "input": []any{map[string]any{"role": "user", "content": parts}}}
}

func TestHTTPMediaPreservesBytesRolesAndOrder(t *testing.T) {
	server, _, paths := mediaFixture(t, "media-accept")
	request := mediaRequest("alpha/org/model", map[string]any{"type": "input_text", "text": "before"}, imagePart(), map[string]any{"type": "input_text", "text": "after"}, map[string]any{"type": "input_file", "filename": "note.pdf", "file_data": "data:application/pdf;base64,Yg=="})
	request["instructions"] = "current instruction"
	request["input"] = append(request["input"].([]any), map[string]any{"role": "assistant", "content": "reply"}, map[string]any{"role": "user", "content": "next"})
	status, v := post(t, server.URL, request)
	if status != 200 || v["status"] != "completed" {
		t.Fatal(status, v)
	}
	raw, err := os.ReadFile(paths["alpha"])
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Prompt []map[string]any `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatal(err)
	}
	b := captured.Prompt
	if len(b) != 14 || b[0]["text"] != "configured instruction" || !strings.Contains(b[1]["text"].(string), "current instruction") {
		t.Fatal(b)
	}
	if b[3]["text"] != "before" || b[4]["type"] != "image" || b[4]["mimeType"] != "image/png" || b[4]["data"] != "YQ==" || b[5]["text"] != "after" {
		t.Fatal(b)
	}
	resource := b[6]["resource"].(map[string]any)
	if resource["mimeType"] != "application/pdf" || resource["blob"] != "Yg==" || resource["uri"] != "acpboss-attachment:///message/0/part/3/note.pdf" {
		t.Fatal(resource)
	}
	if !strings.Contains(b[8]["text"].(string), `"role":"assistant"`) || b[9]["text"] != "reply" || b[12]["text"] != "next" {
		t.Fatal(b)
	}
}

func TestHTTPMediaRejectionBeforePrompt(t *testing.T) {
	server, h, paths := mediaFixture(t, "media-accept")
	cases := []struct {
		name, model string
		part        any
	}{
		{"unsupported image", "beta/org/model", imagePart()},
		{"unsupported file", "beta/org/model", map[string]any{"type": "input_file", "filename": "a.pdf", "file_data": "data:application/pdf;base64,YQ=="}},
		{"invalid base64", "alpha/org/model", map[string]any{"type": "input_image", "image_url": "data:image/png;base64,notbase64"}},
		{"wrong MIME", "alpha/org/model", map[string]any{"type": "input_image", "image_url": "data:text/plain;base64,YQ=="}},
		{"remote URL", "alpha/org/model", map[string]any{"type": "input_image", "image_url": "https://127.0.0.1/private"}},
		{"conflicting sources", "alpha/org/model", map[string]any{"type": "input_image", "image_url": "data:image/png;base64,YQ==", "file_id": "file-1"}},
		{"quality detail", "alpha/org/model", map[string]any{"type": "input_image", "image_url": "data:image/png;base64,YQ==", "detail": "high"}},
		{"file id", "alpha/org/model", map[string]any{"type": "input_file", "file_id": "file-1"}},
		{"audio extension", "alpha/org/model", map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "YQ==", "format": "wav"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, v := post(t, server.URL, mediaRequest(tc.model, tc.part))
			if status != 400 || v["output"] != nil || v["error"].(map[string]any)["param"] == nil {
				t.Fatal(status, v)
			}
		})
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid media invoked provider", path, err)
		}
	}
	h.router.Config.Settings.MaxMessageBytes = 1024
	status, v := post(t, server.URL, mediaRequest("alpha/org/model", map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("YQ==", 400)}))
	if status != 413 {
		t.Fatal(status, v)
	}
	// A rejected request must not poison later sessions/provider prompts.
	h.router.Config.Settings.MaxMessageBytes = 8 << 20
	status, v = post(t, server.URL, map[string]any{"model": "beta/org/model", "store": false, "input": "ok"})
	if status != 200 || v["status"] != "completed" {
		t.Fatal(status, v)
	}
}

func TestHTTPMediaOutputFailsExplicitly(t *testing.T) {
	server, _, _ := mediaFixture(t, "media-output")
	request := map[string]any{"model": "alpha/org/model", "store": false, "input": "output"}
	status, v := post(t, server.URL, request)
	if status != 200 || v["status"] != "failed" || responseText(t, v) != "partial text" || v["error"] == nil {
		t.Fatal(status, v)
	}
	request["stream"] = true
	raw, _ := json.Marshal(request)
	res, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var frames strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		frames.Write(buf[:n])
		if err != nil {
			break
		}
	}
	stream := frames.String()
	if strings.Count(stream, "event: response.failed\n") != 1 || strings.Contains(stream, "event: response.completed\n") || !strings.Contains(stream, "partial text") {
		t.Fatal(stream)
	}
}
