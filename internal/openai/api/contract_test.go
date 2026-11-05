package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGeneratedContract(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	schema, err := RequestSchema()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"text", `{"model":"a/model","input":"hello","store":false}`, true},
		{"image", `{"model":"a/model","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,YQ=="}]}],"store":false}`, true},
		{"file", `{"model":"a/model","input":[{"role":"user","content":[{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,YQ=="}]}],"store":false}`, true},
		{"missing store", `{"model":"a/model","input":"hello"}`, true},
		{"conversation string", `{"model":"a/model","input":"hello","conversation":"conv_a"}`, true},
		{"conversation reference", `{"model":"a/model","input":"hello","conversation":{"id":"conv_a"}}`, true},
		{"empty conversation", `{"model":"a/model","input":"hello","conversation":""}`, false},
		{"invalid reference", `{"model":"a/model","input":"hello","conversation":{"id":"conv_a","items":[]}}`, false},
		{"store true", `{"model":"a/model","input":"hello","store":true}`, false},
		{"nested extra", `{"model":"a/model","input":[{"role":"user","content":[{"type":"input_text","text":"hi","extra":true}]}],"store":false}`, false},
		{"remote image", `{"model":"a/model","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}],"store":false}`, false},
		{"detail", `{"model":"a/model","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,YQ==","detail":"high"}]}],"store":false}`, false},
		{"file id", `{"model":"a/model","input":[{"role":"user","content":[{"type":"input_file","file_id":"file-1"}]}],"store":false}`, false},
		{"continuation", `{"model":"a/model","input":"hello","store":false,"previous_response_id":"resp_1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.body), &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestChatCompletionRequestSchema(t *testing.T) {
	schema, err := Schema("CreateChatCompletion")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"text", `{"model":"a/model","messages":[{"role":"user","content":"hello"}]}`, true},
		{"stream", `{"model":"a/model","messages":[{"role":"system","content":"brief"},{"role":"developer","content":"exact"},{"role":"user","content":"hello"}],"stream":true}`, true},
		{"inert values", `{"model":"a/model","messages":[{"role":"user","content":"hello"}],"n":1,"store":false,"modalities":["text"]}`, true},
		{"content parts", `{"model":"a/model","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ==","detail":"auto"}}]}]}`, true},
		{"missing model", `{"messages":[{"role":"user","content":"hello"}]}`, false},
		{"empty messages", `{"model":"a/model","messages":[]}`, false},
		{"empty content", `{"model":"a/model","messages":[{"role":"user","content":""}]}`, false},
		{"multiple choices", `{"model":"a/model","messages":[{"role":"user","content":"hello"}],"n":2}`, false},
		{"storage", `{"model":"a/model","messages":[{"role":"user","content":"hello"}],"store":true}`, false},
		{"sampling", `{"model":"a/model","messages":[{"role":"user","content":"hello"}],"temperature":0}`, false},
		{"tools", `{"model":"a/model","messages":[{"role":"user","content":"hello"}],"tools":[]}`, false},
		{"audio modality", `{"model":"a/model","messages":[{"role":"user","content":"hello"}],"modalities":["audio"]}`, false},
		{"remote image", `{"model":"a/model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`, false},
		{"image on assistant", `{"model":"a/model","messages":[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}}]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.body), &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestConversationRequestSchemas(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"CreateConversation", `{}`, true},
		{"CreateConversation", `{"metadata":{"topic":"demo"}}`, true},
		{"CreateConversation", `{"metadata":null}`, true},
		{"CreateConversation", `{"items":[]}`, false},
		{"CreateConversation", `{"metadata":{"topic":1}}`, false},
		{"UpdateConversation", `{}`, false},
		{"UpdateConversation", `{"metadata":null}`, true},
		{"UpdateConversation", `{"metadata":{"topic":"next"}}`, true},
	} {
		schema, err := Schema(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal([]byte(tc.body), &value); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); (err == nil) != tc.valid {
			t.Fatalf("%s %s: %v", tc.name, tc.body, err)
		}
	}
	schema, err := Schema("CreateConversation")
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []map[string]string{
		{strings.Repeat("k", 65): "v"},
		{"k": strings.Repeat("v", 513)},
	} {
		if err := schema.Validate(map[string]any{"metadata": metadataValue(metadata)}); err == nil {
			t.Fatal("oversized metadata accepted")
		}
	}
	metadata := map[string]string{}
	for i := range 17 {
		metadata[fmt.Sprint(i)] = "v"
	}
	if err := schema.Validate(map[string]any{"metadata": metadataValue(metadata)}); err == nil {
		t.Fatal("too many metadata keys accepted")
	}
}
func metadataValue(value map[string]string) any {
	raw, _ := json.Marshal(value)
	var result any
	_ = json.Unmarshal(raw, &result)
	return result
}
