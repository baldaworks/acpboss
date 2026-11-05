package api

import (
	"encoding/json"
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
		{"missing store", `{"model":"a/model","input":"hello"}`, false},
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
