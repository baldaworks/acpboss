package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/baldaworks/acpboss/internal/openai/api"
)

type eventStream struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
	sequence   int64
	timeout    time.Duration
}

func newStream(w http.ResponseWriter, timeout time.Duration) *eventStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	return &eventStream{writer: w, controller: http.NewResponseController(w), timeout: timeout}
}
func (s *eventStream) emit(ctx context.Context, kind string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := s.controller.SetWriteDeadline(deadline); err != nil && err != http.ErrNotSupported {
		return err
	}
	var payload any
	switch v := value.(type) {
	case api.ResponseEvent:
		v.Type = api.ResponseEventType(kind)
		v.SequenceNumber = s.sequence
		payload = v
	case api.OutputItemEvent:
		v.Type = api.OutputItemEventType(kind)
		v.SequenceNumber = s.sequence
		payload = v
	case api.ContentPartEvent:
		v.Type = api.ContentPartEventType(kind)
		v.SequenceNumber = s.sequence
		payload = v
	case api.TextDeltaEvent:
		v.Type = api.TextDeltaEventType(kind)
		v.SequenceNumber = s.sequence
		payload = v
	case api.TextDoneEvent:
		v.Type = api.TextDoneEventType(kind)
		v.SequenceNumber = s.sequence
		payload = v
	default:
		return fmt.Errorf("unsupported SSE payload")
	}
	s.sequence++
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", kind, b); err != nil {
		return err
	}
	return s.controller.Flush()
}
func (s *eventStream) begin(ctx context.Context, b *builder) error {
	if err := s.emit(ctx, "response.created", api.ResponseEvent{Response: b.response}); err != nil {
		return err
	}
	if err := s.emit(ctx, "response.in_progress", api.ResponseEvent{Response: b.response}); err != nil {
		return err
	}
	m := b.message
	m.Content = []api.ResponseText{}
	if err := s.emit(ctx, "response.output_item.added", api.OutputItemEvent{Item: m}); err != nil {
		return err
	}
	return s.emit(ctx, "response.content_part.added", api.ContentPartEvent{ItemId: b.message.Id, Part: b.message.Content[0]})
}
func (s *eventStream) delta(ctx context.Context, b *builder, text string) error {
	if text == "" {
		return nil
	}
	return s.emit(ctx, "response.output_text.delta", api.TextDeltaEvent{ItemId: b.message.Id, Delta: text, Logprobs: []any{}})
}
func (s *eventStream) finish(ctx context.Context, b *builder, final response) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.emit(ctx, "response.output_text.done", api.TextDoneEvent{ItemId: b.message.Id, Text: b.message.Content[0].Text, Logprobs: []any{}}); err != nil {
		return err
	}
	if err := s.emit(ctx, "response.content_part.done", api.ContentPartEvent{ItemId: b.message.Id, Part: b.message.Content[0]}); err != nil {
		return err
	}
	if err := s.emit(ctx, "response.output_item.done", api.OutputItemEvent{Item: b.message}); err != nil {
		return err
	}
	return s.emit(ctx, "response."+string(final.Status), api.ResponseEvent{Response: final})
}
