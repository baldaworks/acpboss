package openai

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"time"

	"github.com/baldaworks/acpboss/internal/openai/api"
)

type chatCompletionResponseFunc func(http.ResponseWriter)

func (f chatCompletionResponseFunc) VisitCreateChatCompletionResponse(w http.ResponseWriter) error {
	f(w)
	return nil
}

func (h *Handler) CreateChatCompletion(ctx context.Context, request api.CreateChatCompletionRequestObject) (api.CreateChatCompletionResponseObject, error) {
	n, err := normalizeChatCompletion(*request.Body)
	if err != nil {
		var input *inputError
		_ = errors.As(err, &input)
		return chatCompletionResponseFunc(func(w http.ResponseWriter) {
			writeError(w, http.StatusBadRequest, input.message, input.param, "unsupported_or_invalid_parameter")
		}), nil
	}
	return chatCompletionResponseFunc(func(w http.ResponseWriter) {
		if n.Stream {
			h.respondChatCompletionStream(ctx, w, n)
			return
		}
		h.respondChatCompletion(ctx, w, n)
	}), nil
}

type chatBuilder struct {
	value api.ChatCompletion
	text  boundedText
}

func newChatBuilder(n normalized, limit int) *chatBuilder {
	return &chatBuilder{
		value: api.ChatCompletion{
			Id:      "chatcmpl-" + rand.Text(),
			Object:  api.ChatCompletionObjectChatCompletion,
			Created: time.Now().Unix(),
			Model:   n.Model,
			Choices: []api.ChatCompletionChoice{},
		},
		text: newBoundedText(limit),
	}
}

func (b *chatBuilder) finish(reason string) (api.ChatCompletion, error) {
	finish, err := chatFinishReason(reason)
	if err != nil {
		return api.ChatCompletion{}, err
	}
	b.value.Choices = []api.ChatCompletionChoice{{
		Index:        api.ChatCompletionChoiceIndexN0,
		FinishReason: api.ChatCompletionChoiceFinishReason(finish),
		Logprobs:     nil,
		Message: api.ChatCompletionMessage{
			Role:    api.ChatCompletionMessageRoleAssistant,
			Content: b.text.String(),
			Refusal: nil,
		},
	}}
	return b.value, nil
}

func chatFinishReason(reason string) (string, error) {
	switch reason {
	case "end_turn":
		return "stop", nil
	case "max_tokens":
		return "length", nil
	default:
		return "", errors.New("provider stopped without successful completion")
	}
}

func (h *Handler) respondChatCompletion(parent context.Context, w http.ResponseWriter, n normalized) {
	b := newChatBuilder(n, h.router.Config.Settings.MaxMessageBytes)
	result, err := h.runTurn(parent, n, turnCallbacks{delta: func(_ context.Context, text string) error {
		return b.text.append(text)
	}})
	if err != nil {
		writeTurnError(w, err)
		return
	}
	if parent.Err() != nil {
		return
	}
	if result.err != nil {
		writeError(w, http.StatusBadGateway, "upstream prompt failed", "", "server_error")
		return
	}
	value, err := b.finish(result.reason)
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider stopped without successful completion", "", "server_error")
		return
	}
	writeJSON(w, http.StatusOK, value)
}
