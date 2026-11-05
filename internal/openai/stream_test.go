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

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestOfficialSDKStreamingResponse(t *testing.T) {
	server, _ := httpFixture(t)
	client := responses.NewResponseService(option.WithBaseURL(server.URL+"/v1/"), option.WithAPIKey("fixture"), option.WithMaxRetries(0))
	stream := client.NewStreaming(t.Context(), responses.ResponseNewParams{Model: shared.ResponsesModel("beta/other"), Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("hello")}, Store: param.NewOpt(false)})
	defer func() { _ = stream.Close() }()
	var text strings.Builder
	var final string
	terminals := 0
	sequence := int64(-1)
	types := []string{}
	for stream.Next() {
		e := stream.Current()
		types = append(types, e.Type)
		if e.SequenceNumber != sequence+1 {
			t.Fatal("unordered event", e.SequenceNumber, sequence)
		}
		sequence = e.SequenceNumber
		switch e.Type {
		case "response.output_text.delta":
			text.WriteString(e.AsResponseOutputTextDelta().Delta)
		case "response.completed":
			terminals++
			final = e.AsResponseCompleted().Response.OutputText()
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if text.String() != "beta:other:hello" || final != text.String() || terminals != 1 {
		t.Fatal(text.String(), final, terminals)
	}
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"}
	a, _ := json.Marshal(types)
	b, _ := json.Marshal(want)
	if !bytes.Equal(a, b) {
		t.Fatal(types)
	}
}

func TestRawStreamIdentityFailureAndEmpty(t *testing.T) {
	server, _ := httpFixture(t)
	for _, tc := range []struct{ input, status string }{{"hello", "completed"}, {"partial-fail", "failed"}, {"permission", "failed"}, {"limit", "incomplete"}, {"empty", "completed"}} {
		t.Run(tc.input, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"model": "alpha/org/model", "input": tc.input, "store": false, "stream": true})
			res, err := http.Post(server.URL+"/v1/responses", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatal(res.StatusCode)
			}
			scanner := bufio.NewScanner(res.Body)
			var event string
			seq := float64(-1)
			responseID, itemID := "", ""
			var text strings.Builder
			terminals := 0
			var snapshot map[string]any
			terminalSeen := false
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "[DONE]") || strings.Contains(line, "chat.completion.chunk") {
					t.Fatal(line)
				}
				if strings.HasPrefix(line, "event: ") {
					event = strings.TrimPrefix(line, "event: ")
				}
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				if terminalSeen {
					t.Fatal("event after terminal")
				}
				var e map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
					t.Fatal(err)
				}
				if e["type"] != event || e["sequence_number"] != seq+1 {
					t.Fatal(e)
				}
				seq = e["sequence_number"].(float64)
				switch event {
				case "response.created":
					responseID = e["response"].(map[string]any)["id"].(string)
				case "response.output_item.added":
					itemID = e["item"].(map[string]any)["id"].(string)
				case "response.output_text.delta":
					if e["item_id"] != itemID || e["output_index"] != float64(0) || e["content_index"] != float64(0) {
						t.Fatal(e)
					}
					text.WriteString(e["delta"].(string))
				case "response.completed", "response.failed", "response.incomplete":
					terminalSeen = true
					terminals++
					snapshot = e["response"].(map[string]any)
					if snapshot["id"] != responseID || snapshot["status"] != tc.status {
						t.Fatal(snapshot)
					}
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if terminals != 1 || responseText(t, snapshot) != text.String() {
				t.Fatal(terminals, snapshot, text.String())
			}
			if tc.input == "empty" && text.Len() != 0 {
				t.Fatal(text.String())
			}
			if tc.status != "completed" && snapshot["output"].([]any)[0].(map[string]any)["status"] == "completed" {
				t.Fatal("failed item labelled completed")
			}
		})
	}
}

func TestStreamDisconnectReleasesAdmission(t *testing.T) {
	server, h := httpFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	body := strings.NewReader(`{"model":"alpha/org/model","input":"hang","store":false,"stream":true}`)
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/responses", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(res.Body)
	if !scanner.Scan() {
		t.Fatal("missing start event")
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
	status, v := post(t, server.URL, map[string]any{"model": "beta/org/model", "input": "hello", "store": false})
	if status != 200 || v["status"] != "completed" {
		t.Fatal(status, v)
	}
}
