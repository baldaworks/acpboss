package openai

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/acpboss/internal/openai/api"
)

type response = api.Response
type outputMessage = api.OutputMessage
type outputText = api.ResponseText
type responseError = api.ResponseError
type incompleteDetails = api.IncompleteDetails
type builder struct {
	response response
	message  outputMessage
	limit    int
	text     strings.Builder
}

func newBuilder(n normalized, limit int) *builder {
	b := &builder{response: response{Id: "resp_" + rand.Text(), Object: "response", CreatedAt: time.Now().Unix(), Status: "in_progress", Instructions: n.Instructions, Model: n.Model, Output: []outputMessage{}, Tools: []any{}, ToolChoice: "none", Text: textOptions()}, message: outputMessage{Id: "msg_" + rand.Text(), Type: "message", Role: "assistant", Status: "in_progress", Content: []outputText{{Type: "output_text", Annotations: []any{}, Logprobs: []any{}}}}, limit: limit}
	if n.Conversation != "" {
		b.response.Conversation = &api.ConversationReference{Id: n.Conversation}
	}
	return b
}
func (b *builder) append(text string) error {
	if len(text)+b.text.Len() > b.limit {
		return fmt.Errorf("response text exceeds configured limit")
	}
	_, _ = b.text.WriteString(text)
	return nil
}
func (b *builder) finish(reason string, err error) response {
	b.message.Content[0].Text = b.text.String()
	b.response.Status = "completed"
	b.message.Status = "completed"
	if err != nil {
		b.response.Status = "failed"
		b.message.Status = "incomplete"
		b.response.Error = &responseError{Code: "server_error", Message: err.Error()}
	} else if reason == "max_tokens" {
		b.response.Status = "incomplete"
		b.message.Status = "incomplete"
		b.response.IncompleteDetails = &incompleteDetails{Reason: "max_output_tokens"}
	} else if reason != "end_turn" {
		b.response.Status = "failed"
		b.message.Status = "incomplete"
		b.response.Error = &responseError{Code: "server_error", Message: "provider stopped without successful completion"}
	}
	now := time.Now().Unix()
	if b.response.Status == "completed" {
		b.response.CompletedAt = &now
	}
	b.response.Output = []outputMessage{b.message}
	return b.response
}

func textOptions() api.TextOptions {
	var options api.TextOptions
	options.Format.Type = "text"
	return options
}
