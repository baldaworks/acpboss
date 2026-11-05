package openai

import (
	"context"

	"github.com/baldaworks/acpboss/internal/proxy"
)

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
