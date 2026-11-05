package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/baldaworks/acpboss/internal/openai/api"
)

type chatStream struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	identity   api.ChatCompletionChunk
}

func newChatStream(w http.ResponseWriter, b *chatBuilder, timeout time.Duration) *chatStream {
	return &chatStream{
		writer:     w,
		controller: http.NewResponseController(w),
		timeout:    timeout,
		identity: api.ChatCompletionChunk{
			Id:      b.value.Id,
			Object:  api.ChatCompletionChunkObjectChatCompletionChunk,
			Created: b.value.Created,
			Model:   b.value.Model,
		},
	}
}

func (s *chatStream) deadline(ctx context.Context) error {
	deadline := time.Now().Add(s.timeout)
	if current, ok := ctx.Deadline(); ok && current.Before(deadline) {
		deadline = current
	}
	if err := s.controller.SetWriteDeadline(deadline); err != nil && err != http.ErrNotSupported {
		return err
	}
	return nil
}

func (s *chatStream) emit(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.deadline(ctx); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.writer, "data: %s\n\n", payload); err != nil {
		return err
	}
	return s.controller.Flush()
}

func (s *chatStream) chunk(ctx context.Context, delta api.ChatCompletionChunkDelta, finish *string) error {
	value := s.identity
	value.Choices = []api.ChatCompletionChunkChoice{{
		Index:        api.ChatCompletionChunkChoiceIndexN0,
		Delta:        delta,
		Logprobs:     nil,
		FinishReason: finish,
	}}
	return s.emit(ctx, value)
}

func (s *chatStream) begin(ctx context.Context) error {
	s.writer.Header().Set("Content-Type", "text/event-stream")
	s.writer.Header().Set("Cache-Control", "no-cache")
	s.writer.Header().Set("X-Accel-Buffering", "no")
	role := api.ChatCompletionChunkDeltaRoleAssistant
	return s.chunk(ctx, api.ChatCompletionChunkDelta{Role: &role}, nil)
}

func (s *chatStream) delta(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	return s.chunk(ctx, api.ChatCompletionChunkDelta{Content: &text}, nil)
}

func (s *chatStream) finish(ctx context.Context, finish string) error {
	if err := s.chunk(ctx, api.ChatCompletionChunkDelta{}, &finish); err != nil {
		return err
	}
	if err := s.deadline(ctx); err != nil {
		return err
	}
	if _, err := fmt.Fprint(s.writer, "data: [DONE]\n\n"); err != nil {
		return err
	}
	return s.controller.Flush()
}

func (s *chatStream) fail(ctx context.Context) error {
	return s.emit(ctx, errorValue(http.StatusBadGateway, "upstream prompt failed", "", "server_error"))
}

func (h *Handler) respondChatCompletionStream(parent context.Context, w http.ResponseWriter, n normalized) {
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported by HTTP transport", "stream", "streaming_unavailable")
		return
	}
	b := newChatBuilder(n, h.router.Config.Settings.MaxMessageBytes)
	stream := newChatStream(w, b, h.router.Config.Settings.RequestTimeout)
	result, err := h.runTurn(parent, n, turnCallbacks{
		start: func(ctx context.Context) error { return stream.begin(ctx) },
		delta: func(ctx context.Context, text string) error {
			if err := b.text.append(text); err != nil {
				return err
			}
			return stream.delta(ctx, text)
		},
	})
	if err != nil {
		writeTurnError(w, err)
		return
	}
	if parent.Err() != nil {
		return
	}
	if result.err != nil {
		_ = stream.fail(parent)
		return
	}
	finish, err := chatFinishReason(result.reason)
	if err != nil {
		_ = stream.fail(parent)
		return
	}
	_ = stream.finish(parent, finish)
}
