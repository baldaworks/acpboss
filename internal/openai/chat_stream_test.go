package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

func TestOfficialSDKChatCompletionStreaming(t *testing.T) {
	server, _ := httpFixture(t)
	client := openaisdk.NewClient(
		option.WithBaseURL(server.URL+"/v1/"),
		option.WithAPIKey("fixture"),
		option.WithMaxRetries(0),
	)
	stream := client.Chat.Completions.NewStreaming(t.Context(), openaisdk.ChatCompletionNewParams{
		Model: shared.ChatModel("beta/other"),
		Messages: []openaisdk.ChatCompletionMessageParamUnion{{
			OfUser: &openaisdk.ChatCompletionUserMessageParam{
				Content: openaisdk.ChatCompletionUserMessageParamContentUnion{OfString: openaisdk.String("hello")},
			},
		}},
	})
	defer func() { _ = stream.Close() }()
	var text strings.Builder
	identity := ""
	roles := 0
	terminals := 0
	for stream.Next() {
		chunk := stream.Current()
		if identity == "" {
			identity = chunk.ID
		}
		if chunk.ID != identity || !strings.HasPrefix(chunk.ID, "chatcmpl-") || string(chunk.Object) != "chat.completion.chunk" || chunk.Model != "beta/other" {
			t.Fatal(chunk)
		}
		if len(chunk.Choices) != 1 {
			t.Fatal(chunk.Choices)
		}
		choice := chunk.Choices[0]
		if choice.Delta.Role == "assistant" {
			roles++
		}
		text.WriteString(choice.Delta.Content)
		if choice.FinishReason != "" {
			terminals++
			if choice.FinishReason != "stop" {
				t.Fatal(choice)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if text.String() != "beta:other:hello" || roles != 1 || terminals != 1 {
		t.Fatal(text.String(), roles, terminals)
	}
}

func readRawChatStream(t *testing.T, serverURL, input string) ([]map[string]any, int, int) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "alpha/org/model",
		"messages": []any{map[string]any{"role": "user", "content": input}},
		"stream":   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Post(serverURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(res.StatusCode, res.Header)
	}
	var frames []map[string]any
	done := 0
	events := 0
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			events++
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			done++
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return frames, done, events
}

func TestRawChatCompletionStreamTerminalBehavior(t *testing.T) {
	server, h := httpFixture(t)
	for _, tc := range []struct {
		input, finish string
		done, errors  int
	}{
		{"hello", "stop", 1, 0},
		{"limit", "length", 1, 0},
		{"empty", "stop", 1, 0},
		{"partial-fail", "", 0, 1},
	} {
		t.Run(tc.input, func(t *testing.T) {
			frames, done, events := readRawChatStream(t, server.URL, tc.input)
			if done != tc.done || events != 0 || len(frames) < 2 {
				t.Fatal(done, events, frames)
			}
			id := ""
			terminals := 0
			errors := 0
			for _, frame := range frames {
				if frame["error"] != nil {
					errors++
					continue
				}
				if id == "" {
					id = frame["id"].(string)
				}
				if frame["id"] != id || frame["object"] != "chat.completion.chunk" || frame["model"] != "alpha/org/model" {
					t.Fatal(frame)
				}
				choice := frame["choices"].([]any)[0].(map[string]any)
				if choice["finish_reason"] != nil {
					terminals++
					if choice["finish_reason"] != tc.finish {
						t.Fatal(choice)
					}
				}
			}
			if errors != tc.errors || terminals != 1-tc.errors {
				t.Fatal(errors, terminals, frames)
			}
		})
	}

	h.router.Config.Settings.MaxMessageBytes = 1000
	frames, done, _ := readRawChatStream(t, server.URL, "burst")
	if done != 0 || frames[len(frames)-1]["error"] == nil {
		t.Fatal(done, frames[len(frames)-1])
	}
}

func TestChatCompletionStreamSetupErrorPrecedesSSEHeaders(t *testing.T) {
	server, _ := httpFixture(t)
	body := strings.NewReader(`{"model":"missing/model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	res, err := http.Post(server.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound || strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(res.StatusCode, res.Header)
	}
	var value map[string]any
	if err := json.NewDecoder(res.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value["error"].(map[string]any)["code"] != "provider_setup_failed" {
		t.Fatal(value)
	}
}

func TestChatCompletionStreamDisconnectReleasesAdmission(t *testing.T) {
	server, h := httpFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	body := strings.NewReader(`{"model":"alpha/org/model","messages":[{"role":"user","content":"hang"}],"stream":true}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(res.Body)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "data: ") {
		t.Fatal("missing initial chunk")
	}
	cancel()
	_ = res.Body.Close()
	deadline := time.Now().Add(time.Second)
	for len(h.slots) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("disconnect retained request admission")
		}
		time.Sleep(time.Millisecond)
	}
	status, value := chatPost(t, server.URL, map[string]any{"model": "beta/other", "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
	if status != http.StatusOK || value["object"] != "chat.completion" {
		t.Fatal(status, value)
	}
}
