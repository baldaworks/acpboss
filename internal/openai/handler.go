package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/baldaworks/acpboss/internal/openai/api"
	"github.com/baldaworks/acpboss/internal/proxy"
	acp "github.com/coder/acp-go-sdk"
)

// Handler serves Responses, live conversation handles and the model catalog.
type Handler struct {
	router        *proxy.Router
	slots         chan struct{}
	transport     http.Handler
	conversations *conversations
}

// New returns a handler with request admission that also covers session setup.
func New(r *proxy.Router) *Handler {
	h := &Handler{router: r, slots: make(chan struct{}, r.Config.Settings.MaxInFlight), conversations: newConversations(r.Config.Settings.MaxSessions)}
	h.transport = api.Handler(api.NewStrictHandlerWithOptions(h, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, 400, "invalid request body", "", "invalid_request")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, 500, "response transport failed", "", "server_error")
		},
	}))
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" && r.Method == http.MethodGet {
		h.transport.ServeHTTP(w, r)
		return
	}

	schemaName, allowed := requestRoute(r.URL.Path, r.Method)
	if allowed == "" {
		writeError(w, 404, "unsupported route", "", "unsupported_endpoint")
		return
	}
	if schemaName == "invalid-method" {
		w.Header().Set("Allow", allowed)
		writeError(w, 405, "method not allowed", "", "method_not_allowed")
		return
	}
	if schemaName == "" {
		h.transport.ServeHTTP(w, r)
		return
	}

	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeError(w, 400, "Content-Type must be application/json", "", "invalid_request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(h.router.Config.Settings.MaxMessageBytes))
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			writeError(w, 413, "request body exceeds limit", "", "request_too_large")
		} else {
			writeError(w, 400, "cannot read request body", "", "invalid_request")
		}
		return
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		writeError(w, 400, "invalid JSON", "", "invalid_request")
		return
	}
	schema, err := api.Schema(schemaName)
	if err != nil {
		writeError(w, 500, "HTTP contract unavailable", "", "server_error")
		return
	}
	if err := schema.Validate(value); err != nil {
		writeError(w, 400, "invalid JSON or unsupported request field", schemaParam(err), "unsupported_or_invalid_parameter")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.transport.ServeHTTP(w, r)
}

func (h *Handler) ListModels(_ context.Context, _ api.ListModelsRequestObject) (api.ListModelsResponseObject, error) {
	data := []api.Model{}
	for _, m := range h.router.Catalog() {
		data = append(data, api.Model{Id: m.ID, Object: "model", OwnedBy: m.ProviderID, Created: 0})
	}
	return api.ListModels200JSONResponse{Object: "list", Data: data}, nil
}

type responseFunc func(http.ResponseWriter)

func (f responseFunc) VisitCreateResponseResponse(w http.ResponseWriter) error { f(w); return nil }

func (h *Handler) CreateResponse(ctx context.Context, request api.CreateResponseRequestObject) (api.CreateResponseResponseObject, error) {
	n, err := normalizeResponse(*request.Body)
	if err != nil {
		var input *inputError
		_ = errors.As(err, &input)
		return responseFunc(func(w http.ResponseWriter) {
			writeError(w, 400, input.message, input.param, "unsupported_or_invalid_parameter")
		}), nil
	}
	return responseFunc(func(w http.ResponseWriter) { h.respond(ctx, w, n) }), nil
}

func (h *Handler) respond(parent context.Context, w http.ResponseWriter, n normalized) {
	if n.Stream {
		if _, ok := w.(http.Flusher); !ok {
			writeError(w, 500, "streaming unsupported by HTTP transport", "stream", "streaming_unavailable")
			return
		}
	}

	b := newBuilder(n, h.router.Config.Settings.MaxMessageBytes)
	var stream *eventStream
	callbacks := turnCallbacks{delta: func(ctx context.Context, text string) error {
		if err := b.append(text); err != nil {
			return err
		}
		if stream != nil {
			return stream.delta(ctx, b, text)
		}
		return nil
	}}
	if n.Stream {
		stream = newStream(w, h.router.Config.Settings.RequestTimeout)
		callbacks.start = func(ctx context.Context) error { return stream.begin(ctx, b) }
	}
	result, err := h.runTurn(parent, n, callbacks)
	if err != nil {
		writeTurnError(w, err)
		return
	}
	if parent.Err() != nil {
		return
	}
	final := b.finish(result.reason, result.err)
	if stream != nil {
		_ = stream.finish(parent, b, final)
		return
	}
	writeJSON(w, 200, final)
}

func writeTurnError(w http.ResponseWriter, err error) {
	var output *turnOutputError
	if errors.As(err, &output) {
		return
	}
	var content *turnContentError
	if errors.As(err, &content) {
		writeError(w, 400, content.Error(), content.param, "unsupported_or_invalid_content")
		return
	}
	writeSetupError(w, err, "model")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	// Generation or setup may have consumed the request budget. Allow a bounded
	// final write so the client still receives its outcome over a real socket.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, message, param, code string) {
	writeJSON(w, status, errorValue(status, message, param, code))
}
func errorValue(status int, message, param, code string) api.Error {
	kind := api.ErrorErrorTypeInvalidRequestError
	if status >= 500 {
		kind = api.ErrorErrorTypeServerError
	}
	var value api.Error
	value.Error.Code = code
	value.Error.Message = message
	value.Error.Type = kind
	if param != "" {
		value.Error.Param = &param
	}
	return value
}

// requestRoute keeps exact paths/method errors stable around generated routing.
func requestRoute(path, method string) (string, string) {
	switch path {
	case "/v1/responses":
		if method == http.MethodPost {
			return "CreateResponse", "POST"
		}
		return "invalid-method", "POST"
	case "/v1/chat/completions":
		if method == http.MethodPost {
			return "CreateChatCompletion", "POST"
		}
		return "invalid-method", "POST"
	case "/v1/conversations":
		if method == http.MethodPost {
			return "CreateConversation", "POST"
		}
		return "invalid-method", "POST"
	}
	id, ok := strings.CutPrefix(path, "/v1/conversations/")
	if !ok || id == "" || id == "." || id == ".." || strings.Contains(id, "/") {
		return "", ""
	}
	switch method {
	case http.MethodGet, http.MethodDelete:
		return "", "GET, POST, DELETE"
	case http.MethodPost:
		return "UpdateConversation", "GET, POST, DELETE"
	default:
		return "invalid-method", "GET, POST, DELETE"
	}
}
func (h *Handler) releaseSession(id string) {
	cleanup, cancel := context.WithTimeout(context.Background(), h.router.Config.Settings.ShutdownTimeout)
	defer cancel()
	_ = h.router.Release(cleanup, id)
}
func writeSetupError(w http.ResponseWriter, err error, param string) {
	status := 502
	var upstream *acp.RequestError
	switch {
	case errors.Is(err, errConversationNotFound):
		writeError(w, 404, "conversation not found", "conversation", "conversation_not_found")
		return
	case errors.Is(err, proxy.ErrUnknownModel):
		status = 404
	case errors.Is(err, proxy.ErrBusy):
		writeError(w, 409, "conversation is busy", "conversation", "conversation_busy")
		return
	case errors.Is(err, proxy.ErrCapacity):
		status = 429
	case errors.Is(err, context.DeadlineExceeded):
		status = 504
	case errors.As(err, &upstream) && upstream.Code == -32000:
		status = 503
	}
	writeError(w, status, "provider setup failed or requested model unavailable", param, "provider_setup_failed")
}
