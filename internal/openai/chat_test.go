package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

func chatPost(t *testing.T, baseURL string, value any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
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

func TestOfficialSDKChatCompletion(t *testing.T) {
	server, _ := httpFixture(t)
	client := openaisdk.NewClient(
		option.WithBaseURL(server.URL+"/v1/"),
		option.WithAPIKey("fixture"),
		option.WithMaxRetries(0),
	)
	result, err := client.Chat.Completions.New(t.Context(), openaisdk.ChatCompletionNewParams{
		Model: shared.ChatModel("beta/other"),
		Messages: []openaisdk.ChatCompletionMessageParamUnion{{
			OfUser: &openaisdk.ChatCompletionUserMessageParam{
				Content: openaisdk.ChatCompletionUserMessageParamContentUnion{OfString: openaisdk.String("hello")},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.ID, "chatcmpl-") || string(result.Object) != "chat.completion" || result.Model != "beta/other" {
		t.Fatal(result)
	}
	if len(result.Choices) != 1 || result.Choices[0].Index != 0 || result.Choices[0].FinishReason != "stop" {
		t.Fatal(result.Choices)
	}
	message := result.Choices[0].Message
	if string(message.Role) != "assistant" || message.Content != "beta:other:hello" {
		t.Fatal(message)
	}
}

func TestChatCompletionShapeRolesAndFailures(t *testing.T) {
	server, h := httpFixture(t)
	status, value := chatPost(t, server.URL, map[string]any{
		"model": "alpha/org/model",
		"messages": []any{
			map[string]any{"role": "system", "content": "brief"},
			map[string]any{"role": "developer", "content": "exact"},
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": "prior"},
			map[string]any{"role": "user", "content": "next"},
		},
		"n": 1, "store": false, "modalities": []any{"text"},
	})
	if status != http.StatusOK || value["object"] != "chat.completion" || value["model"] != "alpha/org/model" {
		t.Fatal(status, value)
	}
	choice := value["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if choice["finish_reason"] != "stop" || choice["logprobs"] != nil || message["role"] != "assistant" || message["refusal"] != nil {
		t.Fatal(choice)
	}
	text := message["content"].(string)
	for _, want := range []string{`"role":"system"`, `"role":"developer"`, `"role":"user"`, `"role":"assistant"`} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	if _, ok := value["usage"]; ok {
		t.Fatal("fabricated usage", value)
	}

	status, value = chatPost(t, server.URL, map[string]any{"model": "alpha/org/model", "messages": []any{map[string]any{"role": "user", "content": "limit"}}})
	if status != http.StatusOK || value["choices"].([]any)[0].(map[string]any)["finish_reason"] != "length" {
		t.Fatal(status, value)
	}
	status, value = chatPost(t, server.URL, map[string]any{"model": "missing/model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
	if status != http.StatusNotFound || value["error"].(map[string]any)["code"] != "provider_setup_failed" {
		t.Fatal(status, value)
	}
	status, value = chatPost(t, server.URL, map[string]any{"model": "alpha/org/model", "messages": []any{map[string]any{"role": "user", "content": "partial-fail"}}})
	if status != http.StatusBadGateway || value["error"].(map[string]any)["code"] != "server_error" {
		t.Fatal(status, value)
	}
	h.router.Config.Settings.MaxMessageBytes = 1000
	status, value = chatPost(t, server.URL, map[string]any{"model": "alpha/org/model", "messages": []any{map[string]any{"role": "user", "content": "burst"}}})
	if status != http.StatusBadGateway || value["error"].(map[string]any)["code"] != "server_error" {
		t.Fatal(status, value)
	}
}

func TestChatCompletionMediaAndRoleValidation(t *testing.T) {
	server, _, paths := mediaFixture(t, "media-accept")
	request := map[string]any{
		"model": "alpha/org/model",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "before"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,YQ==", "detail": "auto"}},
				map[string]any{"type": "text", "text": "after"},
			}},
			map[string]any{"role": "assistant", "content": "reply"},
			map[string]any{"role": "user", "content": "next"},
		},
	}
	status, value := chatPost(t, server.URL, request)
	if status != http.StatusOK {
		t.Fatal(status, value)
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
	blocks := captured.Prompt
	if len(blocks) != 13 || blocks[0]["text"] != "configured instruction" || blocks[3]["text"] != "before" || blocks[4]["type"] != "image" || blocks[4]["data"] != "YQ==" || blocks[5]["text"] != "after" {
		t.Fatal(blocks)
	}
	if !strings.Contains(blocks[7]["text"].(string), `"role":"assistant"`) || blocks[8]["text"] != "reply" || blocks[11]["text"] != "next" {
		t.Fatal(blocks)
	}

	request["messages"] = []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,YQ=="}}}}}
	status, value = chatPost(t, server.URL, request)
	if status != http.StatusBadRequest || value["error"].(map[string]any)["param"] == nil {
		t.Fatal(status, value)
	}
}
