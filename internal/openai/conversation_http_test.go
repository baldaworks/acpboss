package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdkconversations "github.com/openai/openai-go/v3/conversations"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

func resourceRequest(t *testing.T, method, address string, value any) (int, map[string]any) {
	t.Helper()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, address, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
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
func createHandle(t *testing.T, address string) string {
	t.Helper()
	status, value := resourceRequest(t, http.MethodPost, address+"/v1/conversations", map[string]any{})
	if status != 200 {
		t.Fatal(status, value)
	}
	return value["id"].(string)
}
func TestConversationHTTPResourceLifecycle(t *testing.T) {
	server, h := httpFixture(t)
	client := sdkconversations.NewConversationService(option.WithBaseURL(server.URL+"/v1/"), option.WithAPIKey("fixture"), option.WithMaxRetries(0))
	value, err := client.New(t.Context(), sdkconversations.ConversationNewParams{Metadata: shared.Metadata{"topic": "first"}})
	if err != nil || !strings.HasPrefix(value.ID, "conv_") || value.Metadata.(map[string]any)["topic"] != "first" {
		t.Fatal(value, err)
	}
	sid, release, err := h.conversations.acquire(value.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := h.router.Snapshot(sid); err != nil {
		t.Fatal("missing native logical session", err)
	}
	got, err := client.Get(t.Context(), value.ID)
	if err != nil || got.ID != value.ID {
		t.Fatal(got, err)
	}
	got, err = client.Update(t.Context(), value.ID, sdkconversations.ConversationUpdateParams{Metadata: shared.Metadata{"topic": "second"}})
	if err != nil || got.Metadata.(map[string]any)["topic"] != "second" {
		t.Fatal(got, err)
	}
	status, raw := resourceRequest(t, http.MethodPost, server.URL+"/v1/conversations/"+value.ID, map[string]any{"metadata": nil})
	if status != 200 || raw["metadata"] != nil {
		t.Fatal(status, raw)
	}
	second := createHandle(t, server.URL)
	h.router.Config.Settings.MaxSessions = 2
	if _, err := client.New(t.Context(), sdkconversations.ConversationNewParams{}); err == nil {
		t.Fatal("conversation capacity not enforced")
	}
	deleted, err := client.Delete(t.Context(), value.ID)
	if err != nil || !deleted.Deleted || deleted.ID != value.ID {
		t.Fatal(deleted, err)
	}
	if _, err := h.router.Snapshot(sid); err == nil {
		t.Fatal("deleted native route retained")
	}
	if _, err := client.Get(t.Context(), value.ID); err == nil {
		t.Fatal("deleted handle exists")
	}
	if _, err := client.Get(t.Context(), second); err != nil {
		t.Fatal("unrelated handle lost", err)
	}
	createHandle(t, server.URL) // Explicit deletion frees native capacity.
	for _, tc := range []struct {
		method, path string
		body         any
		status       int
	}{
		{"DELETE", "/v1/conversations/" + value.ID, nil, 404},
		{"POST", "/v1/conversations/" + value.ID, map[string]any{"metadata": nil}, 404},
		{"POST", "/v1/conversations", map[string]any{"items": []any{}}, 400},
		{"GET", "/v1/conversations/" + second + "/items", nil, 404},
		{"GET", "/v1/responses/resp_old", nil, 404},
		{"GET", "/v1/conversations", nil, 405},
		{"PUT", "/v1/conversations/" + second, nil, 405},
	} {
		status, value := resourceRequest(t, tc.method, server.URL+tc.path, tc.body)
		if status != tc.status {
			t.Fatal(tc, status, value)
		}
	}
}

func TestConversationResponsesNativeHistory(t *testing.T) {
	server, _ := httpFixture(t)
	handle := createHandle(t, server.URL)
	for _, tc := range []struct {
		model, want string
		selector    any
	}{
		{"alpha/org/model", "alpha:org/model:0", handle},
		{"alpha/other", "alpha:other:1", map[string]any{"id": handle}},
		{"beta/org/model", "beta:org/model:0", handle},
		{"alpha/org/model", "alpha:org/model:2", handle},
	} {
		status, result := post(t, server.URL, map[string]any{"model": tc.model, "input": "history", "conversation": tc.selector})
		if status != 200 || result["status"] != "completed" || responseText(t, result) != tc.want {
			t.Fatal(status, result)
		}
		if result["conversation"].(map[string]any)["id"] != handle || result["store"] != false {
			t.Fatal(result)
		}
	}
	for _, selector := range []any{nil, nil} {
		status, result := post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "history", "conversation": selector})
		if status != 200 || responseText(t, result) != "alpha:org/model:0" {
			t.Fatal(status, result)
		}
	}
	status, result := post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "history", "conversation": "conv_missing"})
	if status != 404 || result["error"].(map[string]any)["param"] != "conversation" {
		t.Fatal(status, result)
	}
}
