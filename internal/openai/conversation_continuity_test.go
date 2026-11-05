package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestConversationForwardsOnlyCurrentInput(t *testing.T) {
	server, _, paths := mediaFixture(t, "media-accept")
	handle := createHandle(t, server.URL)
	var backendID string
	for _, input := range []string{"remember private-nonce-519", "what was the token?"} {
		status, result := post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": input, "conversation": handle})
		if status != 200 || result["status"] != "completed" {
			t.Fatal(status, result)
		}
		raw, err := os.ReadFile(paths["alpha"])
		if err != nil {
			t.Fatal(err)
		}
		var captured struct {
			SessionID string           `json:"sessionId"`
			Prompt    []map[string]any `json:"prompt"`
		}
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Fatal(err)
		}
		if len(captured.Prompt) != 2 || captured.Prompt[0]["text"] != "configured instruction" || captured.Prompt[1]["text"] != input {
			t.Fatal("replayed or changed prompt", string(raw))
		}
		if backendID != "" && backendID != captured.SessionID {
			t.Fatal("native session replaced")
		}
		backendID = captured.SessionID
	}
}

func TestOfficialSDKConversationStreaming(t *testing.T) {
	server, _ := httpFixture(t)
	handle := createHandle(t, server.URL)
	client := responses.NewResponseService(option.WithBaseURL(server.URL+"/v1/"), option.WithAPIKey("fixture"), option.WithMaxRetries(0))
	selectors := []responses.ResponseNewParamsConversationUnion{
		{OfString: param.NewOpt(handle)},
		{OfConversationObject: &responses.ResponseConversationParam{ID: handle}},
	}
	for i, selector := range selectors {
		stream := client.NewStreaming(t.Context(), responses.ResponseNewParams{Model: shared.ResponsesModel("alpha/org/model"), Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("history")}, Conversation: selector})
		terminal := 0
		for stream.Next() {
			e := stream.Current()
			switch e.Type {
			case "response.created":
				if e.AsResponseCreated().Response.Conversation.ID != handle {
					t.Fatal("missing initial reference")
				}
			case "response.completed":
				final := e.AsResponseCompleted().Response
				want := []string{"alpha:org/model:0", "alpha:org/model:1"}[i]
				if final.Conversation.ID != handle || final.OutputText() != want {
					t.Fatal(final.RawJSON())
				}
				terminal++
			}
		}
		err := stream.Err()
		_ = stream.Close()
		if err != nil || terminal != 1 {
			t.Fatal(err, terminal)
		}
	}
}

// Wait for an actual upstream chunk before competing with or deleting the turn.
func startConversationHang(t *testing.T, address, handle string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	raw, _ := json.Marshal(map[string]any{"model": "alpha/org/model", "input": "hang", "conversation": handle, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address+"/v1/responses", bytes.NewReader(raw))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"delta":"waiting"`) {
			return res, cancel
		}
	}
	cancel()
	_ = res.Body.Close()
	t.Fatal("no upstream hang chunk", scanner.Err())
	return nil, nil
}
func TestConversationBusyDeleteAndDisconnect(t *testing.T) {
	server, h := httpFixture(t)
	handle := createHandle(t, server.URL)
	other := createHandle(t, server.URL)
	res, cancel := startConversationHang(t, server.URL, handle)
	defer cancel()
	defer func() { _ = res.Body.Close() }()
	status, value := post(t, server.URL, map[string]any{"model": "beta/other", "input": "hello", "conversation": handle})
	if status != 409 {
		t.Fatal(status, value)
	}
	status, value = post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "history", "conversation": other})
	if status != 200 || responseText(t, value) != "alpha:org/model:0" {
		t.Fatal(status, value)
	}
	status, value = resourceRequest(t, http.MethodDelete, server.URL+"/v1/conversations/"+handle, nil)
	if status != 200 {
		t.Fatal(status, value)
	}
	cancel()
	status, value = post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "hello", "conversation": handle})
	if status != 404 {
		t.Fatal(status, value)
	}
	// Disconnect cancels the native prompt but leaves the live conversation bound.
	sid, finish, err := h.conversations.acquire(other)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	res2, cancel2 := startConversationHang(t, server.URL, other)
	cancel2()
	_ = res2.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, value = post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "history", "conversation": other})
		if status != 409 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation never drained")
		}
		time.Sleep(time.Millisecond)
	}
	if status != 200 || responseText(t, value) != "alpha:org/model:1" {
		t.Fatal(status, value)
	}
	same, release, err := h.conversations.acquire(other)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if same != sid {
		t.Fatal("disconnect replaced native session")
	}
	// A new HTTP serving instance has no alias recovery/index.
	fresh := New(h.router)
	if _, err := fresh.conversations.get(other); err == nil {
		t.Fatal("handle survived registry restart")
	}
}

func TestConversationRejectedMediaKeepsNativeHistory(t *testing.T) {
	server, _ := httpFixture(t)
	handle := createHandle(t, server.URL)
	request := mediaRequest("alpha/org/model", imagePart())
	request["conversation"] = handle
	status, value := post(t, server.URL, request)
	if status != 400 {
		t.Fatal(status, value)
	}
	status, value = post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "history", "conversation": handle})
	if status != 200 || responseText(t, value) != "alpha:org/model:0" {
		t.Fatal(status, value)
	}
}

func TestConversationCancellationWaitsForNativeDrain(t *testing.T) {
	server, h := httpFixtureModes(t, map[string]string{"alpha": "unresponsive", "beta": "legacy"})
	handle := createHandle(t, server.URL)
	sid, release, err := h.conversations.acquire(handle)
	if err != nil {
		t.Fatal(err)
	}
	release()
	res, cancel := startConversationHang(t, server.URL, handle)
	cancel()
	_ = res.Body.Close()
	// The HTTP lease can end before an unresponsive native RPC finishes. Router
	// busy state must still prevent a second selection/prompt on this session.
	status, value := post(t, server.URL, map[string]any{"model": "alpha/other", "input": "history", "conversation": handle})
	if status != 409 {
		t.Fatal("native drain did not retain exclusion", status, value)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, value = post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "history", "conversation": handle})
		if status != 409 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native RPC never drained")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != 200 || responseText(t, value) != "alpha:org/model:0" {
		t.Fatal(status, value)
	}
	same, release, err := h.conversations.acquire(handle)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if same != sid {
		t.Fatal("native session recreated after cancellation")
	}
}

func TestConversationDeadBindingIsNotRecreated(t *testing.T) {
	server, _ := httpFixture(t)
	handle := createHandle(t, server.URL)
	status, value := post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "crash", "conversation": handle})
	if status != 200 || value["status"] != "failed" {
		t.Fatal(status, value)
	}
	status, value = post(t, server.URL, map[string]any{"model": "alpha/org/model", "input": "history", "conversation": handle})
	if status != 404 {
		t.Fatal("dead binding silently recreated", status, value)
	}
	status, value = post(t, server.URL, map[string]any{"model": "beta/org/model", "input": "history", "conversation": handle})
	if status != 200 || responseText(t, value) != "beta:org/model:0" {
		t.Fatal(status, value)
	}
}
