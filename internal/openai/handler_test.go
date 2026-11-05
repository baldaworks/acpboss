package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/acpboss/internal/proxy"
	"github.com/baldaworks/acpboss/internal/testprovider"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestACPFixtureProcess(t *testing.T) { testprovider.MaybeRun() }
func httpFixture(t *testing.T) (*httptest.Server, *Handler) {
	t.Helper()
	return httpFixtureModes(t, map[string]string{"alpha": "", "beta": "legacy"})
}
func httpFixtureModes(t *testing.T, modes map[string]string) (*httptest.Server, *Handler) {
	t.Helper()
	c := testprovider.Config(t, modes)
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
	return server, h
}
func post(t *testing.T, url string, value any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(url+"/v1/responses", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var result map[string]any
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, result
}
func responseText(t *testing.T, v map[string]any) string {
	t.Helper()
	items, ok := v["output"].([]any)
	if !ok || len(items) != 1 {
		t.Fatal(v)
	}
	message := items[0].(map[string]any)
	return message["content"].([]any)[0].(map[string]any)["text"].(string)
}

func TestOfficialSDKBufferedResponse(t *testing.T) {
	server, _ := httpFixture(t)
	client := responses.NewResponseService(option.WithBaseURL(server.URL+"/v1/"), option.WithAPIKey("fixture"), option.WithMaxRetries(0))
	got, err := client.New(t.Context(), responses.ResponseNewParams{Model: shared.ResponsesModel("beta/other"), Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("hello")}, Store: param.NewOpt(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Object != "response" || got.Model != "beta/other" || got.Status != "completed" || got.OutputText() != "beta:other:hello" {
		t.Fatalf("SDK response mismatch: %s %s %s %q", got.Object, got.Model, got.Status, got.OutputText())
	}
	var wire struct {
		Store bool `json:"store"`
	}
	if err := json.Unmarshal([]byte(got.RawJSON()), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Store {
		t.Fatal("stored response claimed")
	}
}

func TestInputReplayAndInstructions(t *testing.T) {
	server, _ := httpFixture(t)
	status, first := post(t, server.URL, map[string]any{"model": "alpha/org/model", "store": false, "instructions": "be precise", "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "first"}}}}})
	if status != 200 {
		t.Fatal(first)
	}
	original := responseText(t, first)
	items := first["output"].([]any)
	items = append(items, map[string]any{"role": "user", "content": "second"})
	status, next := post(t, server.URL, map[string]any{"model": "alpha/org/model", "store": false, "instructions": "new instructions", "input": items})
	if status != 200 {
		t.Fatal(next)
	}
	text := responseText(t, next)
	index := strings.Index(text, "\n")
	if index < 0 {
		t.Fatal(text)
	}
	var transcript struct {
		Instructions string
		Messages     []textMessage
	}
	if err := json.Unmarshal([]byte(text[index+1:]), &transcript); err != nil {
		t.Fatal(err)
	}
	if transcript.Instructions != "new instructions" || len(transcript.Messages) != 2 || transcript.Messages[0].Role != "assistant" || transcript.Messages[0].Text != original || transcript.Messages[1].Text != "second" {
		t.Fatal("history lost, duplicated or contaminated", transcript)
	}
}

func TestUnsupportedBeforeAcceptance(t *testing.T) {
	server, _ := httpFixture(t)
	for name, extra := range map[string]map[string]any{"stored": {"store": true}, "continuation": {"previous_response_id": "resp_missing"}, "background": {"background": true}, "tools": {"tools": []any{map[string]any{"type": "function"}}}, "generation": {"temperature": 0.5}, "item reference": {"input": []any{map[string]any{"type": "item_reference", "id": "msg_old"}}}, "image": {"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "x"}}}}}} {
		t.Run(name, func(t *testing.T) {
			request := map[string]any{"model": "beta/org/model", "store": false, "input": "hello"}
			for k, v := range extra {
				request[k] = v
			}
			status, v := post(t, server.URL, request)
			if status != 400 || v["error"] == nil || v["output"] != nil {
				t.Fatal(status, v)
			}
		})
	}
	status, v := post(t, server.URL, map[string]any{"model": "unknown/model", "input": "hi", "store": false})
	if status != 404 {
		t.Fatal(status, v)
	}
	res, err := http.Get(server.URL + "/v1/responses/resp_missing")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 404 {
		t.Fatal(res.StatusCode)
	}
}

func TestAcceptedFailureAndIncomplete(t *testing.T) {
	server, _ := httpFixture(t)
	for _, tc := range []struct{ input, status string }{{"partial-fail", "failed"}, {"permission", "failed"}, {"limit", "incomplete"}} {
		code, v := post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": tc.input, "store": false})
		if code != 200 || v["status"] != tc.status {
			t.Fatal(code, v)
		}
		if v["usage"] != nil {
			t.Fatal("usage fabricated")
		}
		if tc.input == "partial-fail" && responseText(t, v) != "alpha:org/model:" {
			t.Fatal(v)
		}
	}
}

func TestCatalogCapacityAndBodyLimit(t *testing.T) {
	server, h := httpFixture(t)
	res, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct{ Data []struct{ ID string } }
	if err := json.NewDecoder(res.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if len(catalog.Data) != 4 {
		t.Fatal(catalog)
	}
	for range cap(h.slots) {
		h.slots <- struct{}{}
	}
	status, v := post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "hello", "store": false})
	if status != 429 {
		t.Fatal(status, v)
	}
	for range cap(h.slots) {
		<-h.slots
	}
	body := io.LimitReader(strings.NewReader(strings.Repeat("x", h.router.Config.Settings.MaxMessageBytes+1)), int64(h.router.Config.Settings.MaxMessageBytes+1))
	res, err = http.Post(server.URL+"/v1/responses", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 413 {
		t.Fatal(res.StatusCode)
	}
}

func TestGeneratedTransportKeepsStrictContract(t *testing.T) {
	server, _ := httpFixture(t)
	for _, body := range []string{
		`{"model":"alpha/org/model","input":"x","store":false,"Store":true}`,
		`{"model":"alpha/org/model","input":[{"role":"user","content":[{"type":"input_text","text":"x","extra":"secret"}]}],"store":false}`,
		`{"model":"alpha/org/model","input":"x","store":false} {}`,
	} {
		res, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 400 || strings.Contains(string(raw), "secret") {
			t.Fatal(res.StatusCode, string(raw))
		}
	}
	status, value := post(t, server.URL, map[string]any{"model": "alpha/org/model", "store": false, "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "x", "annotations": []any{}, "logprobs": []any{}}}}}})
	if status != 200 || value["status"] != "completed" {
		t.Fatal(status, value)
	}
	for _, route := range []struct {
		method, path string
		status       int
	}{{"GET", "/v1/chat/completions", 405}, {"POST", "/v1/completions", 404}, {"GET", "/v1/responses", 405}, {"HEAD", "/v1/models", 404}, {"POST", "/v1/models", 404}} {
		req, err := http.NewRequest(route.method, server.URL+route.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != route.status {
			t.Fatal(route, res.StatusCode)
		}
	}
}
