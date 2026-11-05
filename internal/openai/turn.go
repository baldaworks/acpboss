package openai

import (
	"context"
	"errors"

	"github.com/baldaworks/acpboss/internal/proxy"
)

type turnCallbacks struct {
	start func(context.Context) error
	delta func(context.Context, string) error
}

type turnResult struct {
	reason string
	err    error
}

type turnContentError struct {
	param string
	err   error
}

func (e *turnContentError) Error() string { return e.err.Error() }
func (e *turnContentError) Unwrap() error { return e.err }

type turnOutputError struct{ err error }

func (e *turnOutputError) Error() string { return e.err.Error() }
func (e *turnOutputError) Unwrap() error { return e.err }

// prepareTurn reserves selection and prompting together. Retained conversations
// release only their lease; isolated requests also release the native session.
func (h *Handler) prepareTurn(ctx context.Context, conversationID, model string) (string, func(), error) {
	if conversationID == "" {
		id, err := h.router.PrepareHTTP(ctx, model)
		if err != nil {
			return "", nil, err
		}
		return id, func() { h.releaseSession(id) }, nil
	}
	id, finish, err := h.conversations.acquire(conversationID)
	if err != nil {
		return "", nil, err
	}
	found := false
	for _, entry := range h.router.Catalog() {
		if entry.ID == model {
			found = true
			break
		}
	}
	if !found {
		finish()
		return "", nil, proxy.ErrUnknownModel
	}
	if err := h.router.Select(ctx, id, model); err != nil {
		finish()
		return "", nil, err
	}
	return id, finish, nil
}

// runTurn owns the transport-independent HTTP request lifecycle. Endpoint
// adapters retain their own request normalization and output envelopes.
func (h *Handler) runTurn(parent context.Context, n normalized, callbacks turnCallbacks) (turnResult, error) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return turnResult{}, proxy.ErrCapacity
	}
	ctx, cancel := context.WithTimeout(parent, h.router.Config.Settings.RequestTimeout)
	defer cancel()
	id, finish, err := h.prepareTurn(ctx, n.Conversation, n.Model)
	if err != nil {
		return turnResult{}, err
	}
	defer finish()
	blocks := n.Blocks
	if blocks == nil {
		blocks = proxy.TextPrompt(n.Prompt)
	}
	if err := h.router.ValidatePrompt(id, blocks); err != nil {
		var content *proxy.ContentError
		if errors.As(err, &content) {
			param := n.SourceRoot
			if content.Index < len(n.Sources) {
				param = n.Sources[content.Index]
			}
			return turnResult{}, &turnContentError{param: param, err: content}
		}
		return turnResult{}, err
	}
	if callbacks.start != nil {
		if err := callbacks.start(ctx); err != nil {
			return turnResult{}, &turnOutputError{err: err}
		}
	}
	result, promptErr := h.router.Prompt(ctx, id, blocks, func(note map[string]any) error {
		update, _ := note["update"].(map[string]any)
		if update["sessionUpdate"] == "agent_message_chunk" {
			content, _ := update["content"].(map[string]any)
			if content["type"] != "text" {
				return errors.New("provider returned assistant content unsupported by HTTP; use the ACP frontend for multimodal output")
			}
		}
		if callbacks.delta == nil {
			return nil
		}
		return callbacks.delta(ctx, proxy.Text(note))
	})
	reason, _ := result["stopReason"].(string)
	return turnResult{reason: reason, err: promptErr}, nil
}
