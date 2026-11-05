package openai

import (
	"context"
	"net/http"

	"github.com/baldaworks/acpboss/internal/openai/api"
)

type conversationResponseFunc func(http.ResponseWriter)

func (f conversationResponseFunc) VisitCreateConversationResponse(w http.ResponseWriter) error {
	f(w)
	return nil
}
func (f conversationResponseFunc) VisitGetConversationResponse(w http.ResponseWriter) error {
	f(w)
	return nil
}
func (f conversationResponseFunc) VisitUpdateConversationResponse(w http.ResponseWriter) error {
	f(w)
	return nil
}
func (f conversationResponseFunc) VisitDeleteConversationResponse(w http.ResponseWriter) error {
	f(w)
	return nil
}
func conversationNotFound() conversationResponseFunc {
	return func(w http.ResponseWriter) {
		writeError(w, 404, "conversation not found", "conversation", "conversation_not_found")
	}
}

func (h *Handler) CreateConversation(parent context.Context, request api.CreateConversationRequestObject) (api.CreateConversationResponseObject, error) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return conversationResponseFunc(func(w http.ResponseWriter) { writeError(w, 429, "proxy capacity exhausted", "", "capacity_exceeded") }), nil
	}
	ctx, cancel := context.WithTimeout(parent, h.router.Config.Settings.RequestTimeout)
	defer cancel()
	sid, err := h.router.Create(ctx, h.router.Config.Settings.WorkingDir, nil, nil, "")
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if sid != "" {
			h.releaseSession(sid)
		}
		return conversationResponseFunc(func(w http.ResponseWriter) { writeSetupError(w, err, "") }), nil
	}
	var metadata map[string]string
	if request.Body.Metadata != nil {
		metadata = map[string]string(*request.Body.Metadata)
	}
	value, err := h.conversations.add(sid, metadata)
	if err != nil {
		h.releaseSession(sid)
		return conversationResponseFunc(func(w http.ResponseWriter) { writeSetupError(w, err, "") }), nil
	}
	return api.CreateConversation200JSONResponse(value), nil
}
func (h *Handler) GetConversation(_ context.Context, request api.GetConversationRequestObject) (api.GetConversationResponseObject, error) {
	value, err := h.conversations.get(request.ConversationId)
	if err != nil {
		return conversationNotFound(), nil
	}
	return api.GetConversation200JSONResponse(value), nil
}
func (h *Handler) UpdateConversation(_ context.Context, request api.UpdateConversationRequestObject) (api.UpdateConversationResponseObject, error) {
	var metadata map[string]string
	if request.Body.Metadata != nil {
		metadata = map[string]string(*request.Body.Metadata)
	}
	value, err := h.conversations.update(request.ConversationId, metadata)
	if err != nil {
		return conversationNotFound(), nil
	}
	return api.UpdateConversation200JSONResponse(value), nil
}
func (h *Handler) DeleteConversation(_ context.Context, request api.DeleteConversationRequestObject) (api.DeleteConversationResponseObject, error) {
	sid, err := h.conversations.remove(request.ConversationId)
	if err != nil {
		return conversationNotFound(), nil
	}
	h.releaseSession(sid)
	return api.DeleteConversation200JSONResponse{Id: request.ConversationId, Object: "conversation.deleted", Deleted: true}, nil
}
